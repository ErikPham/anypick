package client

import (
	"errors"
	"fmt"
	"os"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// ConfigDocProblem is one reason a client configuration file is not loadable,
// stated in terms the operator can act on rather than the parser's phrasing.
type ConfigDocProblem struct {
	// Line is 1-based, 0 when the parser did not attribute the failure to a
	// position.
	Line int
	// Detail is a single sentence, safe to show in a doctor finding.
	Detail string
}

func (p ConfigDocProblem) Error() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d: %s", p.Line, p.Detail)
	}
	return p.Detail
}

// String makes ConfigDocProblem readable on its own; Error is the same text, so
// a finding and a returned error never disagree.
func (p ConfigDocProblem) String() string { return p.Error() }

// ValidateTOMLConfig reports why a TOML configuration file cannot be loaded, or
// nil when it is sound. It is deliberately read-only: a diagnosis that mutated
// the file would destroy the evidence, and Codex is the component that has to
// accept the repair.
func ValidateTOMLConfig(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return ValidateTOMLDocument(string(raw))
}

// ValidateTOMLDocument is ValidateTOMLConfig over an in-memory document, so the
// checks can be tested without a filesystem.
//
// The parser runs first and is authoritative: it alone decides whether the
// document is sound. The shape check only runs once the parser has already
// failed, to name a cause the parser cannot see — a bare line at the root is
// reported by the parser as a missing key separator, which tells the operator
// nothing about where the text came from. Ordering it this way is also what
// keeps the shape heuristic from rejecting a sound document: a multi-line
// string's body does not look like `key = value`, so a shape check that ran
// first would reject valid TOML. Consulted only on a document already known to
// be broken, it can only re-word a failure, never invent one.
func ValidateTOMLDocument(raw string) error {
	var doc any
	if err := toml.Unmarshal([]byte(raw), &doc); err != nil {
		if problem, ok := tomlShapeProblem(raw); ok {
			return problem
		}
		return tomlParseProblem(err)
	}
	return nil
}

// tomlShapeProblem names the failure AnyPick's own strippers used to produce:
// one marker family is a prefix of another, a strip cut inside the longer marker
// line, and its tail stayed behind as a bare line at the document root.
//
// It is a line-shape heuristic, not a TOML parser, and it is only ever consulted
// on a document the parser has already rejected — see ValidateTOMLDocument.
func tomlShapeProblem(raw string) (ConfigDocProblem, bool) {
	for i, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") || strings.Contains(trimmed, "=") {
			continue
		}
		return ConfigDocProblem{
			Line: i + 1,
			Detail: "not a TOML key = value pair, a [table] header or a comment; " +
				"this is the shape left behind when one managed-block marker is a " +
				"prefix of another and a strip cuts inside the longer marker line " +
				"(anypick-managed-native-profile)",
		}, true
	}
	return ConfigDocProblem{}, false
}

// tomlParseProblem renders the parser's own verdict, which is authoritative
// whenever the shape check has nothing to add.
func tomlParseProblem(err error) error {
	var de *toml.DecodeError
	if errors.As(err, &de) {
		line, _ := de.Position()
		// DecodeError.String is a source window. A parse failure next to
		// experimental_bearer_token would publish the proxy bearer in doctor
		// output and `anypick clients --json`. The line number is enough.
		return ConfigDocProblem{
			Line:   line,
			Detail: "not valid TOML, so the client refuses to start",
		}
	}
	return ConfigDocProblem{Detail: "not valid TOML, so the client refuses to start"}
}
