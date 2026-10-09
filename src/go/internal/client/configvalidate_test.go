package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure this exists for: a marker-family overlap left a bare line at the
// document root, and Codex refused the file with "key with no value". The block
// was still present, so a presence-only Inspect reported the profile as
// configured and doctor said nothing.
func TestValidateTOMLDocumentCatchesLeakedMarkerTail(t *testing.T) {
	corrupt := "-native-profile\n\n# BEGIN anypick-managed-native-profile\n" +
		"model_provider = \"openai\"\n# END anypick-managed-native-profile\n"

	err := ValidateTOMLDocument(corrupt)
	if err == nil {
		t.Fatal("a bare line at the document root was accepted")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("problem does not name the line: %q", err.Error())
	}
	// The shape tier has to name the cause, not just the symptom: the parser
	// would report a separator it did not find, which tells the operator
	// nothing about where the text came from.
	if !strings.Contains(err.Error(), "prefix") {
		t.Errorf("problem does not name the marker-prefix cause: %q", err.Error())
	}
}

// The shape tier is a heuristic and must not claim soundness it does not have.
// These are documents it has no business rejecting.
func TestValidateTOMLDocumentAcceptsSoundDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":            "",
		"only comments":    "# a\n#b\n",
		"plain block":      codexManagedBlock("m", "", false, "http://127.0.0.1:1/v1", "K", ""),
		"native block":     codexNativeProfileBlock("m"),
		"nested values":    "[a.b]\nc = {d = 1}\ne = \"x = y\"\n",
		"array of tables":  "[[items]]\nname = \"x\"\n",
		"multiline string": "k = \"\"\"\nline\n===\nline\n\"\"\"\n",
		"lone table":       "[t]\n",
		"trailing comment": "a = 1 # note\n# tail\n",
		"dotted key":       "a.b.c = 1\n",
	} {
		if err := ValidateTOMLDocument(doc); err != nil {
			t.Errorf("%s: rejected a sound document: %v", name, err)
		}
	}
}

// The parser tier has to reach failures the shape rule does not model, and it
// is the answer reported when only it fires.
func TestValidateTOMLDocumentDoesNotEchoSecrets(t *testing.T) {
	doc := "experimental_bearer_token = \"sk-secret-value\"\nmodel = \"oops\n"
	err := ValidateTOMLDocument(doc)
	if err == nil {
		t.Fatal("accepted a document with an unterminated string")
	}
	if strings.Contains(err.Error(), "sk-secret-value") {
		t.Fatalf("diagnosis quoted the bearer: %q", err.Error())
	}
}

func TestValidateTOMLDocumentParserTierReportsOtherBreakage(t *testing.T) {
	for name, doc := range map[string]string{
		"unterminated string": "k = \"oops\n",
		"duplicate key":       "a = 1\na = 2\n",
		"bad table header":    "[unclosed\n",
	} {
		err := ValidateTOMLDocument(doc)
		if err == nil {
			t.Errorf("%s: accepted a broken document", name)
			continue
		}
		if !strings.Contains(err.Error(), "not valid TOML") {
			t.Errorf("%s: parser problem not attributed to the parser: %q", name, err.Error())
		}
	}
}

// Inspect must stop calling a profile healthy while its config cannot be loaded,
// in both the plain and the named-profile paths.
func TestInspectReportsUnparseableConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	corrupt := "-native-profile\n\n# BEGIN anypick-managed\nmodel = \"m\"\n" +
		"model_provider = \"anypick\"\n# END anypick-managed\n"
	if err := writeFileAt(codexConfigPath(home), corrupt); err != nil {
		t.Fatal(err)
	}

	res, err := newCodex(home).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Present {
		t.Fatal("Present went false: the block is still there, and the caller uses it to decide the profile exists")
	}
	if len(res.Issues) == 0 {
		t.Fatal("a config Codex cannot parse produced no issue")
	}
	t.Logf("Issues=%v", res.Issues)
	if !strings.Contains(res.Issues[0], "line 1") {
		t.Errorf("issue does not point at the offending line: %q", res.Issues[0])
	}
}

// A sound config must stay silent, or every profile starts reporting.
func TestInspectStaysSilentOnSoundConfig(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	if err := writeFileAt(codexConfigPath(home),
		codexManagedBlock("m", "", false, "http://127.0.0.1:1/v1", "K", "")); err != nil {
		t.Fatal(err)
	}
	res, err := newCodex(home).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 0 {
		t.Errorf("sound config produced issues: %v", res.Issues)
	}
}

// Presence follows the same whole-line rule as the strippers. A native block
// is a real managed config. A comment that only mentions the plain marker is not.
func TestInspectPresenceUsesWholeMarkerLines(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	if err := writeFileAt(codexConfigPath(home), codexNativeProfileBlock("m")); err != nil {
		t.Fatal(err)
	}
	res, err := newCodex(home).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Present || len(res.Issues) != 0 {
		t.Fatalf("native block: Present=%v Issues=%v", res.Present, res.Issues)
	}

	mention := "# note about " + codexBeginMarker + "\nmodel = \"m\"\n"
	if err := writeFileAt(codexConfigPath(home), mention); err != nil {
		t.Fatal(err)
	}
	res, err = newCodex(home).Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if res.Present {
		t.Fatalf("a comment mentioning the marker counted as a managed block: %#v", res)
	}
}
func writeFileAt(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
