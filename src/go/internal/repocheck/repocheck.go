// Package repocheck holds the repository-structure gates: references between
// files that no compiler checks.
//
// These checks used to live as one 150-line shell program inside a Makefile
// recipe. Escaped as `$$name` and joined by backslashes, a `#` comment anywhere
// in it silently commented out the rest of the gate — which is how the
// git-ignored-source check, the authority-documents check and the failing exit
// all disappeared for two commits while CODE_REVIEW.md recorded them as landed.
// A gate written in Go cannot be disabled by a comment: the compiler and the
// tests below are the difference between "we wrote a check" and "the check
// runs".
package repocheck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Sentinel identifies the repository root. VERSION is the one file every
// checkout has and no subdirectory has, and cmd/anypick-release already walks
// up to it, so both commands resolve the root the same way.
const Sentinel = "VERSION"

// Finding is one broken reference or disagreement.
type Finding struct {
	// Detail names the problem on one line.
	Detail string
	// Advice, when set, is printed under the detail. A gate that only reports
	// makes the reader reconstruct the fix.
	Advice []string
}

// Section is one named check and its outcome.
//
// Checked lists what the check actually inspected. That list is not decoration:
// every check here parses a file to discover its inputs, and a parse that
// silently matched nothing would otherwise report success over an empty set —
// the failure mode this whole package exists to prevent.
type Section struct {
	Name     string
	Checked  []string
	Findings []Finding
}

// Passed reports whether every section came back clean.
func Passed(sections []Section) bool {
	for _, s := range sections {
		if len(s.Findings) > 0 {
			return false
		}
	}
	return true
}

// Findings flattens every section's findings.
func Findings(sections []Section) []Finding {
	var out []Finding
	for _, s := range sections {
		out = append(out, s.Findings...)
	}
	return out
}

// Root walks up from start until it finds the repository sentinel, so the
// command works from the repository root or from any subdirectory.
func Root(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, Sentinel)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("cannot find %s in %s or any parent directory", Sentinel, start)
		}
		dir = parent
	}
}

// fail builds a finding, so no check has to remember the shape.
func fail(detail string, advice ...string) Finding {
	return Finding{Detail: detail, Advice: advice}
}

// git runs one git command in root and returns its stdout and exit code.
//
// Shelling out is deliberate: git is the one dependency every checkout already
// has, and re-implementing index and blob inspection in Go would mean a second
// parser to keep honest. The exit code comes back too because
// `git check-ignore` uses 1 for "nothing matched", which is a pass.
func git(root, stdin string, args ...string) (stdout string, exit int, err error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, runErr := cmd.Output()
	if runErr == nil {
		return string(raw), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return string(raw), exitErr.ExitCode(), nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = runErr.Error()
	}
	return "", 0, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
}

// readFile reads one file inside the repository, turning a missing or
// unreadable file into a finding: "the file this check parses is gone" is
// precisely what the gate is for, not an error to abort on.
func readFile(root, rel string) (string, *Finding) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		finding := fail(rel+" cannot be read: "+errText(err),
			"this check parses "+rel+"; if it moved, point the check at the new path")
		return "", &finding
	}
	return string(raw), nil
}

// errText trims os.PathError's decoration down to the cause.
func errText(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// exists reports whether a reference inside relDir resolves to something.
// Build-file references are written relative to the file that contains them
// (`../Resources`, `../../go`), so existence has to be resolved the same way.
func exists(root, relDir, reference string) bool {
	if reference == "" {
		return false
	}
	_, err := os.Stat(filepath.Clean(filepath.Join(root, relDir, filepath.FromSlash(reference))))
	return err == nil
}

// uniqueSorted de-duplicates extraction results, so a reference repeated
// throughout a project file is reported once.
func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// capture returns the first capture group of pattern in content.
func capture(pattern *regexp.Regexp, content string) (string, bool) {
	m := pattern.FindStringSubmatch(content)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// captures returns every first capture group of pattern in content.
func captures(pattern *regexp.Regexp, content string) []string {
	var out []string
	for _, m := range pattern.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}

// checkedEmpty reports a check that parsed its input but discovered nothing to
// verify. Every extraction in this package can match zero times, and a check
// over an empty set proves nothing while looking green.
func emptyInput(what, source string) Finding {
	return fail(what+" found nothing in "+source,
		"a check over zero entries proves nothing: either the entries were",
		"removed, or the extraction broke. If the project genuinely has none",
		"left, delete this check.")
}
