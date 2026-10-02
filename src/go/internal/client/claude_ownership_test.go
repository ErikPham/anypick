package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func claudeOwnershipContext(home, endpoint string) *ApplyContext {
	return &ApplyContext{
		Endpoint: endpoint,
		APIKey:   "sk-test",
		Home:     home,
	}
}

func TestClaudeApplyRefusesForeignEnv(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	// A base URL AnyPick never wrote (no marker) must not be clobbered.
	const foreign = `{"env":{"ANTHROPIC_BASE_URL":"https://relay.example.internal"}}`
	if err := os.WriteFile(settings, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := a.Apply(claudeOwnershipContext(home, "http://127.0.0.1:8787"))
	if err == nil {
		t.Fatal("expected CLIENT_CONFIG_FOREIGN, apply succeeded")
	}
	aerr, ok := err.(*anyerr.Error)
	if !ok || aerr.Code != "CLIENT_CONFIG_FOREIGN" {
		t.Fatalf("want CLIENT_CONFIG_FOREIGN, got %v", err)
	}
	if strings.Contains(err.Error(), "relay.example.internal") {
		t.Fatalf("error must not embed config values: %v", err)
	}

	raw, _ := os.ReadFile(settings)
	if string(raw) != foreign {
		t.Fatalf("apply modified file before consent: %s", raw)
	}
}

func TestClaudeApplyOverwritesOwnedMarker(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	managed := `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:1","ANTHROPIC_AUTH_TOKEN":"old"},"` +
		anypickManagedKey + `":{"keys":["ANTHROPIC_BASE_URL","ANTHROPIC_AUTH_TOKEN"]}}`
	if err := os.WriteFile(settings, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(claudeOwnershipContext(home, "http://127.0.0.1:8787")); err != nil {
		t.Fatalf("marker-owned rewrite refused: %v", err)
	}
	raw, _ := os.ReadFile(settings)
	if !strings.Contains(string(raw), "127.0.0.1:8787") {
		t.Fatalf("owned key not updated: %s", raw)
	}
}

func TestClaudeApplyConsentAllowsForeign(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://relay.example.internal"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := claudeOwnershipContext(home, "http://127.0.0.1:8787")
	ctx.AnyPickRoot = t.TempDir()
	ctx.AllowForeignConfig = true
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("consented apply failed: %v", err)
	}
	raw, _ := os.ReadFile(settings)
	if !strings.Contains(string(raw), "127.0.0.1:8787") {
		t.Fatalf("consented apply did not take over: %s", raw)
	}
}

func TestClaudePreflightUnrelatedKeysPass(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"USER_KEY":"keep"},"permissions":{"allow":["Bash"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	inspection, err := a.Preflight(claudeOwnershipContext(home, "http://127.0.0.1:8787"))
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(inspection.Conflicts) != 0 {
		t.Fatalf("unrelated settings reported as conflicts: %+v", inspection.Conflicts)
	}
	if _, err := a.Apply(claudeOwnershipContext(home, "http://127.0.0.1:8787")); err != nil {
		t.Fatalf("apply with unrelated settings failed: %v", err)
	}
	raw, _ := os.ReadFile(settings)
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("post-apply settings unreadable: %s", raw)
	}
	var env map[string]string
	_ = json.Unmarshal(doc["env"], &env)
	if env["USER_KEY"] != "keep" {
		t.Fatalf("user keys dropped: %s", raw)
	}
	if _, ok := doc["permissions"]; !ok {
		t.Fatalf("non-env sections dropped: %s", raw)
	}
}

func TestClaudePreflightMalformedFailsClosed(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	consent := claudeOwnershipContext(home, "http://127.0.0.1:8787")
	consent.AllowForeignConfig = true
	for name, ctx := range map[string]*ApplyContext{
		"preflight": claudeOwnershipContext(home, "http://127.0.0.1:8787"),
		"consent":   consent,
	} {
		if _, err := a.Apply(ctx); err == nil {
			t.Fatalf("%s: expected failure", name)
		} else {
			aerr, ok := err.(*anyerr.Error)
			if !ok || aerr.Code != "CLIENT_CONFIG_INVALID" {
				t.Fatalf("%s: want CLIENT_CONFIG_INVALID, got %v", name, err)
			}
		}
	}
	raw, _ := os.ReadFile(settings)
	if string(raw) != `{oops` {
		t.Fatalf("malformed file was touched: %s", raw)
	}
}

func TestClaudePreflightReportsAllConflicts(t *testing.T) {
	home := t.TempDir()
	a := newClaude(home)
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://a.example","ANTHROPIC_AUTH_TOKEN":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := a.Preflight(claudeOwnershipContext(home, "http://127.0.0.1:8787"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Conflicts) != 2 {
		t.Fatalf("want 2 conflicts, got %+v", inspection.Conflicts)
	}
}
