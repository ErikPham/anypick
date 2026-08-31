package refs

import "testing"

func TestParseRefGrammar(t *testing.T) {
	cases := []struct {
		input   string
		want    Ref
		wantErr bool
	}{
		{"grok/work", AccountRef("grok", "work"), false},
		{"account/grok/work", AccountRef("grok", "work"), false},
		{"codex/personal", AccountRef("codex", "personal"), false},
		{"openrouter-work", GatewayRef("openrouter-work"), false},
		{"gateway/openrouter-work", GatewayRef("openrouter-work"), false},
		{"@work-grok", PresetRef("work-grok"), false},
		{"preset/work-grok", PresetRef("work-grok"), false},
		{"hub:default", ProxyHubRef("default"), false},
		{"hub/default", ProxyHubRef("default"), false},
		{"pool:gemini", AccountPoolRef("gemini"), false},
		{"pool/gemini", AccountPoolRef("gemini"), false},
		{"", Ref{}, true},
		{"@", Ref{}, true},
		{"preset/", Ref{}, true},
		{"hub:", Ref{}, true},
		{"hub:a/b", Ref{}, true},
		{"pool:nope", Ref{}, true},    // unknown provider
		{"unknown/work", Ref{}, true}, // unknown provider with slash
		{"account/grok", Ref{}, true}, // missing name
		{"account//x", Ref{}, true},   // empty provider
		{"gateway/a/b", Ref{}, true},  // gateway cannot contain /
		{"a/b/c", Ref{}, true},        // too many segments for account form
	}
	for _, tc := range cases {
		got, err := ParseRef(tc.input, nil)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRef(%q) = %+v, want error", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRef(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", tc.input, got, tc.want)
		}
	}
}

// TRAP regression: mutationScopeForRef collapses accounts and pools onto their
// provider; serializeRef keeps precise identity. They must never be swapped.
func TestMutationScopeCollapsesOntoProvider(t *testing.T) {
	account := AccountRef("grok", "work")
	pool := AccountPoolRef("grok")

	if got := MutationScopeForRef(account); got != "provider/grok" {
		t.Errorf("MutationScopeForRef(account) = %q, want provider/grok", got)
	}
	if got := MutationScopeForRef(pool); got != "provider/grok" {
		t.Errorf("MutationScopeForRef(pool) = %q, want provider/grok", got)
	}
	if got := SerializeRef(account); got != "account/grok/work" {
		t.Errorf("SerializeRef(account) = %q, want account/grok/work", got)
	}
	if got := SerializeRef(pool); got != "pool/grok" {
		t.Errorf("SerializeRef(pool) = %q, want pool/grok", got)
	}

	// Non-account kinds use the exact identity as their lock scope.
	if got := MutationScopeForRef(GatewayRef("gw")); got != "gateway/gw" {
		t.Errorf("gateway scope = %q", got)
	}
	if got := MutationScopeForRef(ProxyHubRef("h")); got != "hub/h" {
		t.Errorf("hub scope = %q", got)
	}
	if got := MutationScopeForRef(PresetRef("p")); got != "preset/p" {
		t.Errorf("preset scope = %q", got)
	}
}

func TestDisplayRef(t *testing.T) {
	cases := map[Ref]string{
		AccountRef("grok", "work"): "grok/work",
		GatewayRef("gw"):           "gw",
		ProxyHubRef("main"):        "hub:main",
		PresetRef("fast"):          "@fast",
		AccountPoolRef("gemini"):   "pool:gemini",
	}
	for ref, want := range cases {
		if got := DisplayRef(ref); got != want {
			t.Errorf("DisplayRef(%+v) = %q, want %q", ref, got, want)
		}
	}
}

func TestNativeAccountShorthand(t *testing.T) {
	providers := DefaultAccountProviders()
	clients := map[string]bool{"claude": true, "codex": true, "gemini": true, "kiro": true, "opencode": true}

	ref, ok := ParseNativeAccountShorthand("codex/personal", providers, clients)
	if !ok || ref != AccountRef("codex", "personal") {
		t.Errorf("shorthand codex/personal = %+v ok=%v", ref, ok)
	}
	// grok is an account provider but not a client → not a shorthand.
	if _, ok := ParseNativeAccountShorthand("grok/x", providers, clients); ok {
		t.Error("grok/x must not resolve as native shorthand")
	}
	if _, ok := ParseNativeAccountShorthand("@preset", providers, clients); ok {
		t.Error("@preset must not resolve as shorthand")
	}
}
