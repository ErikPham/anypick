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
		"grok":        "grok-api",
		"xai":         "grok-api",
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
