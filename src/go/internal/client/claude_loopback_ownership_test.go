package client

// A managed route always points Claude Code at a loopback proxy (ADR 0006), so
// a loopback base URL is AnyPick's own value even when the marker is missing:
// the user may have rewritten settings.json, or a crash may have landed between
// the write and the marker. Anything else is the user's own configuration and
// must survive without explicit consent.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClaudeEnvSettings writes a well-formed settings.json carrying env.
// The ownership tests that need byte-exact or malformed fixtures write the file
// themselves on purpose.
func writeClaudeEnvSettings(t *testing.T, home string, env map[string]string) string {
	t.Helper()
	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"env": env})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestClaudeLoopbackEndpointIsOwnedWithoutMarker(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:8787",
		"http://localhost:8787",
		"http://[::1]:8787",
		"http://127.0.0.2:1",
	} {
		t.Run(endpoint, func(t *testing.T) {
			home := t.TempDir()
			a := newClaude(home)
			writeClaudeEnvSettings(t, home, map[string]string{
				"ANTHROPIC_BASE_URL":   endpoint,
				"ANTHROPIC_AUTH_TOKEN": "stale-token-from-previous-start",
			})

			inspection, err := a.Preflight(claudeOwnershipContext(home, "http://127.0.0.1:9999"))
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Conflicts) != 0 {
				t.Fatalf("%s is AnyPick's own value, got conflicts %+v", endpoint, inspection.Conflicts)
			}
			if _, err := a.Apply(claudeOwnershipContext(home, "http://127.0.0.1:9999")); err != nil {
				t.Fatalf("repairing AnyPick's own stale token must not need consent: %v", err)
			}
			raw, err := os.ReadFile(settingsPath(home))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "127.0.0.1:9999") {
				t.Fatalf("stale endpoint survived: %s", raw)
			}
		})
	}
}

func TestClaudeRemoteEndpointStaysForeign(t *testing.T) {
	// A non-loopback endpoint is either the user's own login or another local
	// relay reached over the network. Either way AnyPick cannot prove it wrote
	// it, so it must ask.
	for _, endpoint := range []string{
		"https://api.anthropic.com",
		"https://relay.example.internal",
		"http://10.0.0.5:8080",
		"http://[2001:db8::1]:8080",
	} {
		t.Run(endpoint, func(t *testing.T) {
			home := t.TempDir()
			a := newClaude(home)
			writeClaudeEnvSettings(t, home, map[string]string{"ANTHROPIC_BASE_URL": endpoint})

			inspection, err := a.Preflight(claudeOwnershipContext(home, "http://127.0.0.1:8787"))
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Conflicts) == 0 {
				t.Fatalf("%s must be reported as a conflict", endpoint)
			}
			if _, err := a.Apply(claudeOwnershipContext(home, "http://127.0.0.1:8787")); err == nil {
				t.Fatalf("%s was overwritten without consent", endpoint)
			}
		})
	}
}

func TestLoopbackEndpointClassification(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:1":        true,
		"http://localhost":          true,
		"http://LOCALHOST:8080":     true,
		"http://[::1]:8787":         true,
		"https://api.anthropic.com": false,
		"http://192.168.1.10:80":    false,
		"":                          false,
		"not a url":                 false,
		"http://":                   false,
	}
	for raw, want := range cases {
		if got := isLoopbackEndpoint(raw); got != want {
			t.Errorf("isLoopbackEndpoint(%q) = %v, want %v", raw, got, want)
		}
	}
}
