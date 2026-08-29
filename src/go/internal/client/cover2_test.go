package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

func TestClaudeAdapterIdentityAndReset(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)

	if a.ID() != "claude" || a.Name() != "Claude Code" || a.ShortName() != "Claude" {
		t.Fatalf("identity: %q/%q/%q", a.ID(), a.Name(), a.ShortName())
	}
	caps := a.Capabilities()
	if caps.ProtocolPreference != transport.Anthropic || !caps.SupportsIsolatedHome {
		t.Fatalf("capabilities: %+v", caps)
	}

	// Reset is intentionally conservative: it must never touch live config.
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil || string(raw) != `{"env":{}}` {
		t.Fatalf("Reset mutated live config: %q %v", raw, err)
	}
}

func TestClaudeInspectVariants(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)

	// Missing settings file.
	res, err := a.Inspect()
	if err != nil || res.Present || len(res.Issues) != 1 ||
		!strings.Contains(res.Issues[0], "not found") {
		t.Fatalf("missing: %+v %v", res, err)
	}

	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}

	// Unreadable JSON.
	if err := os.WriteFile(settings, []byte(`{oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = a.Inspect()
	if err != nil || res.Present || !strings.Contains(res.Issues[0], "unreadable") {
		t.Fatalf("corrupt: %+v %v", res, err)
	}

	// Valid JSON without the managed marker.
	if err := os.WriteFile(settings, []byte(`{"env":{"X":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = a.Inspect()
	if err != nil || res.Present || !strings.Contains(res.Issues[0], "anypick-managed") {
		t.Fatalf("unmanaged: %+v %v", res, err)
	}

	// Managed block present.
	managed := `{"env":{"X":"1"},"` + anypickManagedKey + `":true}`
	if err := os.WriteFile(settings, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = a.Inspect()
	if err != nil || !res.Present || len(res.Issues) != 0 {
		t.Fatalf("managed: %+v %v", res, err)
	}
}

func TestBareModelID(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"  ":          "",
		"glm-4.7":     "glm-4.7",
		"vendor/name": "name",
		"a/b/c":       "c",
		" x/y ":       "y",
	}
	for in, want := range cases {
		if got := bareModelID(in); got != want {
			t.Errorf("bareModelID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSmallJSONHelpers(t *testing.T) {
	doc := map[string]json.RawMessage{
		"s": json.RawMessage(`"str"`),
		"n": json.RawMessage(`42`),
	}
	if stringField(doc, "s") != "str" || stringField(doc, "n") != "" ||
		stringField(doc, "absent") != "" {
		t.Fatal("stringField semantics")
	}

	if got := firstNonEmpty("", " ", " real "); got != "real" {
		t.Fatalf("firstNonEmpty: %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("firstNonEmpty empty: %q", got)
	}

	files := writeClientEnvFiles(t.TempDir(), "claude",
		map[string]string{"B_KEY": "v2", "A_KEY": "v1"})
	if len(files) != 1 {
		t.Fatalf("env files: %v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	aIdx := strings.Index(text, `export A_KEY="v1"`)
	bIdx := strings.Index(text, `export B_KEY="v2"`)
	if aIdx < 0 || bIdx < 0 || aIdx > bIdx {
		t.Fatalf("keys not sorted or missing:\n%s", text)
	}

	// Unwritable client dir yields no files.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := writeClientEnvFiles(blocker, "claude", nil); got != nil {
		t.Fatalf("expected nil for unwritable dir: %v", got)
	}
}

func TestCodexIdentityAndConfigPathOverride(t *testing.T) {
	home := t.TempDir()
	a := newCodex(home)
	if a.ID() != "codex" || a.Name() != "OpenAI Codex" || a.ShortName() != "Codex" {
		t.Fatalf("identity: %q/%q/%q", a.ID(), a.Name(), a.ShortName())
	}

	// Default lives under the adapter home.
	if got := codexConfigPath(home); got != filepath.Join(home, ".codex", "config.toml") {
		t.Fatalf("default path: %q", got)
	}
	// CODEX_HOME overrides.
	override := t.TempDir()
	t.Setenv("CODEX_HOME", override)
	if got := codexConfigPath(home); got != filepath.Join(override, "config.toml") {
		t.Fatalf("override ignored: %q", got)
	}
}
