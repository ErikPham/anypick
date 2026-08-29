package transport

import "testing"

// INVARIANT #1: capability pairs come from the source adapters. gemini and
// kiro accept only their own provider; only claude/codex/opencode cross
// providers through a proxy.
func TestAccountAdapterPairingMatrix(t *testing.T) {
	all := []string{"claude", "codex", "opencode", "gemini", "kiro"}

	type row struct {
		name     string
		adapter  Adapter
		expected map[string]Capability
	}

	codex := CodexAccountAdapter("codex", "work", true)
	claude := ClaudeAccountAdapter("claude", "work")
	gemini := GeminiAccountAdapter("gemini", "work")
	grok := GrokAccountAdapter("grok", "work")
	opencode := OpenCodeAccountAdapter("opencode", "work")
	kiroWithBin := KiroAccountAdapter("kiro", "work", func() string { return "/usr/local/bin/kirolink" })
	kiroNoBin := KiroAccountAdapter("kiro", "work", func() string { return "" })

	rows := []row{
		{"codex", codex, map[string]Capability{
			"codex": Direct, "claude": Unsupported, "opencode": Unsupported,
			"gemini": Unsupported, "kiro": Unsupported,
		}},
		{"claude", claude, map[string]Capability{
			"claude": Direct, "codex": Unsupported, "opencode": Unsupported,
			"gemini": Unsupported, "kiro": Unsupported,
		}},
		{"gemini", gemini, map[string]Capability{
			"gemini": Direct, "claude": ManagedBuiltinProxy,
			"codex": ManagedBuiltinProxy, "opencode": ManagedBuiltinProxy,
			"kiro": Unsupported,
		}},
		{"grok", grok, map[string]Capability{
			"claude": ManagedBuiltinProxy, "codex": ManagedBuiltinProxy,
			"opencode": ManagedBuiltinProxy, "gemini": Unsupported, "kiro": Unsupported,
		}},
		{"opencode", opencode, map[string]Capability{
			"opencode": Direct, "claude": ManagedBuiltinProxy,
			"codex": ManagedBuiltinProxy, "gemini": Unsupported, "kiro": Unsupported,
		}},
		{"kiro+bin", kiroWithBin, map[string]Capability{
			"kiro": Direct, "claude": ManagedExternalProxy,
			"codex": ManagedExternalProxy, "opencode": ManagedExternalProxy,
			"gemini": Unsupported,
		}},
		{"kiro-nobin", kiroNoBin, map[string]Capability{
			"kiro": Direct, "claude": ExternalManualProxy,
			"codex": ExternalManualProxy, "opencode": ExternalManualProxy,
			"gemini": Unsupported,
		}},
	}

	for _, r := range rows {
		for _, client := range all {
			want, ok := r.expected[client]
			if !ok {
				t.Fatalf("row %s missing expectation for %s", r.name, client)
			}
			if got := r.adapter.TransportFor(client); got != want {
				t.Errorf("%s adapter transportFor(%s) = %s, want %s",
					r.name, client, got, want)
			}
		}
	}
}

func TestCodexPolicyOverride(t *testing.T) {
	withProxy := CodexAccountAdapter("codex", "work", true)
	withoutProxy := CodexAccountAdapter("codex", "work", false)

	if got, _ := ResolveTransportPolicy(withProxy, "codex", PolicyDirect); got != Direct {
		t.Errorf("explicit direct = %s", got)
	}
	if got, err := ResolveTransportPolicy(withProxy, "codex", PolicyProxy); err != nil ||
		got != ManagedBuiltinProxy {
		t.Errorf("explicit proxy = %s err=%v", got, err)
	}
	if _, err := ResolveTransportPolicy(withoutProxy, "codex", PolicyProxy); err == nil {
		t.Error("proxy policy without proxy service must fail")
	}
	if _, err := ResolveTransportPolicy(withoutProxy, "claude", PolicyDirect); err == nil {
		t.Error("direct policy for unsupported pairing must fail")
	}
}

func TestPoolAdapter(t *testing.T) {
	// Default accepted clients are exactly ['claude','codex','opencode'] —
	// even the provider's own native client is unsupported unless listed
	// (pools serve borrowing clients through a proxy only).
	canProxy := ProviderInfo{ID: "gemini", CanProxy: true}
	a := PoolAdapterFor("gemini", canProxy, ManagedBuiltinProxy)

	if got := a.TransportFor("claude"); got != ManagedBuiltinProxy {
		t.Errorf("pool for claude = %s", got)
	}
	if got := a.TransportFor("opencode"); got != ManagedBuiltinProxy {
		t.Errorf("pool for opencode = %s", got)
	}
	if got := a.TransportFor("gemini"); got != Unsupported {
		t.Errorf("pool default must not serve own native client, got %s", got)
	}
	if got := a.TransportFor("kiro"); got != Unsupported {
		t.Errorf("pool for kiro must be unsupported, got %s", got)
	}

	// Codex restricts its pool to its own client (OAuth speaks Responses only).
	codexPool := PoolAdapterFor("codex", ProviderInfo{ID: "codex", CanProxy: true}, ManagedBuiltinProxy, "codex")
	if got := codexPool.TransportFor("codex"); got != ManagedBuiltinProxy {
		t.Errorf("codex pool for codex = %s", got)
	}
	if got := codexPool.TransportFor("claude"); got != Unsupported {
		t.Errorf("codex pool for claude = %s", got)
	}

	// Kiro's pool is served by the external kirolink bridge.
	kiroPool := PoolAdapterFor("kiro", ProviderInfo{ID: "kiro", CanProxy: true}, ManagedExternalProxy)
	if got := kiroPool.TransportFor("codex"); got != ManagedExternalProxy {
		t.Errorf("kiro pool transport = %s", got)
	}

	cannotProxy := ProviderInfo{ID: "claude", CanProxy: false}
	b := PoolAdapterFor("claude", cannotProxy, ManagedBuiltinProxy)
	if got := b.TransportFor("codex"); got != Unsupported {
		t.Errorf("pool of non-proxying provider must be unsupported, got %s", got)
	}
}

func TestGenericAdapterNarrowCrossProvider(t *testing.T) {
	info := ProviderInfo{ID: "vendorx", CanProxy: true}
	a := GenericAccountAdapter("vendorx", "acct", info)

	if got := a.TransportFor("vendorx"); got != Direct {
		t.Errorf("own client = %s, want direct", got)
	}
	if got := a.TransportFor("claude"); got != ManagedBuiltinProxy {
		t.Errorf("cross-provider claude = %s", got)
	}
	if got := a.TransportFor("gemini"); got != Unsupported {
		t.Errorf("cross-provider gemini must be unsupported, got %s", got)
	}
	if got := a.TransportFor("kiro"); got != Unsupported {
		t.Errorf("cross-provider kiro must be unsupported, got %s", got)
	}
}
