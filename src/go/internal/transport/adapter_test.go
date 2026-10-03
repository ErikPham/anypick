package transport

import "testing"

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
	// Default accepted clients are exactly ['claude','codex','opencode','zcode','dsh','commandcode','pi','omp']
	// — even the provider's own native client is unsupported unless listed
	// (pools serve borrowing clients through a proxy only).
	canProxy := ProviderInfo{ID: "gemini", CanProxy: true}
	a := PoolAdapterFor("gemini", canProxy, ManagedBuiltinProxy)

	if got := a.TransportFor("claude"); got != ManagedBuiltinProxy {
		t.Errorf("pool for claude = %s", got)
	}
	if got := a.TransportFor("opencode"); got != ManagedBuiltinProxy {
		t.Errorf("pool for opencode = %s", got)
	}
	if got := a.TransportFor("zcode"); got != ManagedBuiltinProxy {
		t.Errorf("pool for zcode = %s", got)
	}
	if got := a.TransportFor("commandcode"); got != ManagedBuiltinProxy {
		t.Errorf("pool for commandcode = %s", got)
	}
	if got := a.TransportFor("pi"); got != ManagedBuiltinProxy {
		t.Errorf("pool for pi = %s", got)
	}
	if got := a.TransportFor("omp"); got != ManagedBuiltinProxy {
		t.Errorf("pool for omp = %s", got)
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
	if got := a.TransportFor("dsh"); got != ManagedBuiltinProxy {
		t.Errorf("cross-provider dsh = %s", got)
	}
	if got := a.TransportFor("commandcode"); got != ManagedBuiltinProxy {
		t.Errorf("cross-provider commandcode = %s", got)
	}
	if got := a.TransportFor("pi"); got != ManagedBuiltinProxy {
		t.Errorf("cross-provider pi = %s", got)
	}
	if got := a.TransportFor("omp"); got != ManagedBuiltinProxy {
		t.Errorf("cross-provider omp = %s", got)
	}
	if got := a.TransportFor("gemini"); got != Unsupported {
		t.Errorf("cross-provider gemini must be unsupported, got %s", got)
	}
	if got := a.TransportFor("kiro"); got != Unsupported {
		t.Errorf("cross-provider kiro must be unsupported, got %s", got)
	}
}
