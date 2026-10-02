package catalog

import "testing"

func TestCatalogProviderForFirstPartyLogins(t *testing.T) {
	cases := map[string]string{
		"claude":      "anthropic",
		"anthropic":   "anthropic",
		"codex":       "openai",
		"openai":      "openai",
		"gemini":      "gemini-api",
		"google":      "gemini-api",
		"gemini-api":  "gemini-api",
		"grok":        "grok-api",
		"xai":         "grok-api",
		"grok-api":    "grok-api",
		"opencode":    "opencode",
		"opencode-go": "opencode",
		"zen":         "opencode",
		"openrouter":  "openrouter",
		"litellm":     "litellm",
		"local":       "local",
		"custom":      "custom",
		"kiro":        "",
		"unknown":     "",
		"":            "",
	}
	for account, want := range cases {
		if got := CatalogProviderFor(account); got != want {
			t.Errorf("CatalogProviderFor(%q) = %q, want %q", account, got, want)
		}
	}
}

func TestCatalogIdentityForUnknownProvidersKeepsLiveCacheNamespace(t *testing.T) {
	for _, tc := range []struct{ provider, want string }{
		{provider: "codex", want: "openai"},
		{provider: "gemini", want: "gemini-api"},
		{provider: "future-provider", want: "future-provider"},
		{provider: "", want: ""},
	} {
		if got := CatalogIdentityFor(tc.provider); got != tc.want {
			t.Errorf("CatalogIdentityFor(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestOpenCodePolicyIsLiveOnly(t *testing.T) {
	p := Policy("opencode")
	if p == nil {
		t.Fatal("opencode policy missing")
	}
	if p.LiveFetch != "openai" {
		t.Errorf("opencode LiveFetch = %q, want openai", p.LiveFetch)
	}
	if len(p.StaticFallback) != 0 {
		t.Errorf("opencode StaticFallback must stay empty (no vendor-published list), got %v", p.StaticFallback)
	}
}

func TestPolicyResolvesAccountProviderAliases(t *testing.T) {
	for providerID, wantKey := range map[string]string{
		"gemini":     "gemini-api",
		"gemini-api": "gemini-api",
		"claude":     "anthropic",
		"anthropic":  "anthropic",
		"codex":      "openai",
		"openai":     "openai",
		"grok":       "grok-api",
		"grok-api":   "grok-api",
		"opencode":   "opencode",
	} {
		p := Policy(providerID)
		if p == nil {
			t.Errorf("Policy(%q) = nil, want policy for %s", providerID, wantKey)
		}
	}
}
