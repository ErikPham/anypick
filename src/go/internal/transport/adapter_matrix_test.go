package transport_test

import (
	"testing"

	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/transport"
)

// INVARIANT #1: capability pairs come from the account adapters, never from
// the client registry — the registry lists what exists, not what is allowed.
//
// This file is an external test package so it can import internal/client,
// which imports internal/transport. A test in `package transport` cannot see
// the registry at all, and a hand-written client list is what let a new client
// ship unclassified.

func TestAccountAdapterPairingMatrix(t *testing.T) {
	reg := client.NewRegistry()
	if err := client.RegisterBuiltin(reg, t.TempDir()); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	registered := reg.IDs()
	if len(registered) == 0 {
		t.Fatal("client registry is empty; the pairing matrix would pass vacuously")
	}
	for _, id := range []string{"claude", "codex", "gemini", "kiro", "workbuddy"} {
		if !reg.Has(id) {
			t.Errorf("registry lost built-in client %q", id)
		}
	}

	type row struct {
		name     string
		adapter  transport.Adapter
		expected map[string]transport.Capability
	}

	rows := []row{
		{"codex", transport.CodexAccountAdapter("codex", "work", true), map[string]transport.Capability{
			"grok":  transport.Unsupported,
			"codex": transport.Direct, "claude": transport.Unsupported, "opencode": transport.Unsupported,
			"zcode": transport.Unsupported, "gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.Unsupported, "commandcode": transport.Unsupported, "pi": transport.Unsupported, "omp": transport.Unsupported,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"claude", transport.ClaudeAccountAdapter("claude", "work"), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.Direct, "codex": transport.Unsupported, "opencode": transport.Unsupported,
			"zcode": transport.Unsupported, "gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.Unsupported, "commandcode": transport.Unsupported, "pi": transport.Unsupported, "omp": transport.Unsupported,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"gemini", transport.GeminiAccountAdapter("gemini", "work"), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"gemini": transport.Direct, "claude": transport.ManagedBuiltinProxy,
			"codex": transport.ManagedBuiltinProxy, "opencode": transport.ManagedBuiltinProxy,
			"zcode": transport.ManagedBuiltinProxy, "kiro": transport.Unsupported,
			"dsh": transport.ManagedBuiltinProxy, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"grok", transport.GrokAccountAdapter("grok", "work"), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.ManagedBuiltinProxy, "codex": transport.ManagedBuiltinProxy,
			"opencode": transport.ManagedBuiltinProxy, "zcode": transport.ManagedBuiltinProxy,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.ManagedBuiltinProxy, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"opencode", transport.OpenCodeAccountAdapter("opencode", "work"), map[string]transport.Capability{
			"grok":     transport.Unsupported,
			"opencode": transport.Direct, "claude": transport.ManagedBuiltinProxy,
			"codex": transport.ManagedBuiltinProxy, "zcode": transport.Unsupported,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.Unsupported, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"workbuddy", transport.WorkBuddyAccountAdapter("workbuddy", "work"), map[string]transport.Capability{
			"grok":      transport.Unsupported,
			"workbuddy": transport.Direct,
			"claude":    transport.ManagedBuiltinProxy, "codex": transport.ManagedBuiltinProxy,
			"opencode": transport.ManagedBuiltinProxy, "zcode": transport.ManagedBuiltinProxy,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.ManagedBuiltinProxy, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"cline": transport.Unsupported,
		}},
		{"cline", transport.ClineAccountAdapter("cline", "work"), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.ManagedBuiltinProxy, "codex": transport.ManagedBuiltinProxy,
			"opencode": transport.ManagedBuiltinProxy, "zcode": transport.ManagedBuiltinProxy,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.ManagedBuiltinProxy, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"cline": transport.ManagedBuiltinProxy, "workbuddy": transport.Unsupported,
		}},
		{"kiro+bin", transport.KiroAccountAdapter("kiro", "work", func() string { return "/usr/local/bin/kirolink" }), map[string]transport.Capability{
			"grok": transport.Unsupported,
			"kiro": transport.Direct, "claude": transport.ManagedExternalProxy,
			"codex": transport.ManagedExternalProxy, "opencode": transport.ManagedExternalProxy,
			"zcode": transport.ManagedExternalProxy, "gemini": transport.Unsupported,
			"dsh": transport.ManagedExternalProxy, "commandcode": transport.ManagedExternalProxy,
			"pi": transport.ManagedExternalProxy, "omp": transport.ManagedExternalProxy,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"kiro-nobin", transport.KiroAccountAdapter("kiro", "work", func() string { return "" }), map[string]transport.Capability{
			"grok": transport.Unsupported,
			"kiro": transport.Direct, "claude": transport.ExternalManualProxy,
			"codex": transport.ExternalManualProxy, "opencode": transport.ExternalManualProxy,
			"zcode": transport.ExternalManualProxy, "gemini": transport.Unsupported,
			"dsh": transport.ExternalManualProxy, "commandcode": transport.ExternalManualProxy,
			"pi": transport.ExternalManualProxy, "omp": transport.ExternalManualProxy,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},

		// Pool rows. PoolAdapterFor carries its own hardcoded accepted-client
		// list (adapter.go) and no self-Direct branch, so a client added to the
		// registry has to be either swept into that list or deliberately left
		// out. Nothing below the constructor used to say which.
		{"pool/gemini-default", transport.PoolAdapterFor("gemini", transport.ProviderInfo{ID: "gemini", CanProxy: true}, transport.ManagedBuiltinProxy), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.ManagedBuiltinProxy, "codex": transport.ManagedBuiltinProxy,
			"opencode": transport.ManagedBuiltinProxy, "zcode": transport.ManagedBuiltinProxy,
			"dsh": transport.ManagedBuiltinProxy, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"pool/kiro-external", transport.PoolAdapterFor("kiro", transport.ProviderInfo{ID: "kiro", CanProxy: true, HasAdapter: true}, transport.ManagedExternalProxy), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.ManagedExternalProxy, "codex": transport.ManagedExternalProxy,
			"opencode": transport.ManagedExternalProxy, "zcode": transport.ManagedExternalProxy,
			"dsh": transport.ManagedExternalProxy, "commandcode": transport.ManagedExternalProxy,
			"pi": transport.ManagedExternalProxy, "omp": transport.ManagedExternalProxy,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		{"pool/codex-own-client-only", transport.PoolAdapterFor("codex", transport.ProviderInfo{ID: "codex", CanProxy: true}, transport.ManagedBuiltinProxy, "codex"), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"codex":  transport.ManagedBuiltinProxy,
			"claude": transport.Unsupported, "opencode": transport.Unsupported,
			"zcode": transport.Unsupported, "gemini": transport.Unsupported,
			"kiro": transport.Unsupported, "dsh": transport.Unsupported,
			"commandcode": transport.Unsupported, "pi": transport.Unsupported,
			"omp": transport.Unsupported, "cline": transport.Unsupported,
			"workbuddy": transport.Unsupported,
		}},
		{"pool/no-proxy", transport.PoolAdapterFor("claude", transport.ProviderInfo{ID: "claude", CanProxy: false}, transport.ManagedBuiltinProxy), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.Unsupported, "codex": transport.Unsupported,
			"opencode": transport.Unsupported, "zcode": transport.Unsupported,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"dsh": transport.Unsupported, "commandcode": transport.Unsupported,
			"pi": transport.Unsupported, "omp": transport.Unsupported,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
		// Not a call site. PoolAdapterFor coerces any proxy capability that is
		// not ManagedBuiltinProxy or ManagedExternalProxy to ManagedBuiltinProxy,
		// and that branch is otherwise unreachable from a matrix row.
		{"pool/coerced-transport", transport.PoolAdapterFor("gemini", transport.ProviderInfo{ID: "gemini", CanProxy: true}, transport.Direct), map[string]transport.Capability{
			"grok":   transport.Unsupported,
			"claude": transport.ManagedBuiltinProxy, "codex": transport.ManagedBuiltinProxy,
			"opencode": transport.ManagedBuiltinProxy, "zcode": transport.ManagedBuiltinProxy,
			"dsh": transport.ManagedBuiltinProxy, "commandcode": transport.ManagedBuiltinProxy,
			"pi": transport.ManagedBuiltinProxy, "omp": transport.ManagedBuiltinProxy,
			"gemini": transport.Unsupported, "kiro": transport.Unsupported,
			"cline": transport.Unsupported, "workbuddy": transport.Unsupported,
		}},
	}

	// cline and grok are account adapters with no registered client, so the
	// registry cannot supply them; adapters still classify the string and the
	// rows assert it, so they join the sweep explicitly.
	extra := []string{"cline", "grok"}

	all := append([]string(nil), registered...)
	all = append(all, extra...)

	covered := map[string]bool{}
	for _, id := range all {
		covered[id] = true
	}

	for _, r := range rows {
		for _, id := range all {
			want, ok := r.expected[id]
			if !ok {
				t.Errorf("row %s missing expectation for %s", r.name, id)
				continue
			}
			if got := r.adapter.TransportFor(id); got != want {
				t.Errorf("%s adapter transportFor(%s) = %s, want %s", r.name, id, got, want)
			}
		}
		for id := range r.expected {
			if !covered[id] {
				t.Errorf("row %s has an expectation for %s, which no client sweep covers", r.name, id)
			}
		}
	}
}
