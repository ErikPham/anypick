package catalog

// Static model policies ported from src/catalog/providers.ts. Every id is
// vendor-published; alias keys are stable logical names used by client
// overlays. Values only ever resolve inside one provider.

// ModelPolicy mirrors the optional model-policy members of CatalogProvider.
type ModelPolicy struct {
	// SuggestModels maps alias → canonical id (ModelMap).
	SuggestModels map[string]string
	// RoleDefaults maps role id → default model id.
	RoleDefaults map[string]string
	RoleFriendly []string
	// StaticFallback is the offline list; empty for providers whose rollout
	// differs between auth modes (gemini-api).
	StaticFallback []string
	// RoleModelHints maps role id → substrings that identify fitting models.
	RoleModelHints map[string][]string
	// LiveFetch names the vendor wire shape: openai | anthropic | gemini.
	LiveFetch string
}

const anthropicVersion = "2023-06-01"

var anthropicRoleDefaults = map[string]string{
	"default": "claude-sonnet-5",
	"sonnet":  "claude-sonnet-5",
	"opus":    "claude-opus-5",
	"haiku":   "claude-haiku-4-5",
}

var anthropicModels = map[string]string{
	"claude-fable":      "claude-fable-5",
	"claude-opus":       "claude-opus-5",
	"claude-sonnet":     "claude-sonnet-5",
	"claude-haiku":      "claude-haiku-4-5",
	"claude-fable-5":    "claude-fable-5",
	"claude-opus-5":     "claude-opus-5",
	"claude-sonnet-5":   "claude-sonnet-5",
	"claude-haiku-4-5":  "claude-haiku-4-5",
	"claude-opus-4-8":   "claude-opus-4-8",
	"claude-opus-4-7":   "claude-opus-4-7",
	"claude-opus-4-6":   "claude-opus-4-6",
	"claude-sonnet-4-6": "claude-sonnet-4-6",
	"claude-sonnet-4-5": "claude-sonnet-4-5",
}

var openaiModels = map[string]string{
	"gpt":           "gpt-5.6-sol",
	"gpt-5":         "gpt-5.6-sol",
	"gpt-default":   "gpt-5.6-sol",
	"gpt-balanced":  "gpt-5.6-terra",
	"gpt-fast":      "gpt-5.6-luna",
	"gpt-codex":     "gpt-5.3-codex",
	"gpt-5.6-sol":   "gpt-5.6-sol",
	"gpt-5.6-terra": "gpt-5.6-terra",
	"gpt-5.6-luna":  "gpt-5.6-luna",
	"gpt-5.5":       "gpt-5.5",
	"gpt-5.5-pro":   "gpt-5.5-pro",
	"gpt-5.4":       "gpt-5.4",
	"gpt-5.3-codex": "gpt-5.3-codex",
	"gpt-4.1":       "gpt-4.1",
}

var geminiModels = map[string]string{
	"gemini":                 "gemini-3.6-flash",
	"gemini-pro":             "gemini-3.1-pro-preview",
	"gemini-flash":           "gemini-3.6-flash",
	"gemini-lite":            "gemini-3.5-flash-lite",
	"gemini-3.1-pro-preview": "gemini-3.1-pro-preview",
	"gemini-3.6-flash":       "gemini-3.6-flash",
	"gemini-3.5-flash":       "gemini-3.5-flash",
	"gemini-3.5-flash-lite":  "gemini-3.5-flash-lite",
	"gemini-3.1-flash-lite":  "gemini-3.1-flash-lite",
	"gemini-2.5-pro":         "gemini-2.5-pro",
	"gemini-2.5-flash":       "gemini-2.5-flash",
}

var grokModels = map[string]string{
	"grok":                    "grok-4.5",
	"grok-4.5":                "grok-4.5",
	"grok-4.3":                "grok-4.3",
	"grok-4.20":               "grok-4.20-0309-reasoning",
	"grok-4.20-reasoning":     "grok-4.20-0309-reasoning",
	"grok-4.20-non-reasoning": "grok-4.20-0309-non-reasoning",
	"grok-multi-agent":        "grok-4.20-multi-agent-0309",
	"grok-build":              "grok-build-0.1",
}

var openrouterModels = map[string]string{
	"claude-fable":           "anthropic/claude-fable-5",
	"claude-opus":            "anthropic/claude-opus-5",
	"claude-sonnet":          "anthropic/claude-sonnet-5",
	"claude-haiku":           "anthropic/claude-haiku-4.5",
	"claude-fable-5":         "anthropic/claude-fable-5",
	"claude-opus-5":          "anthropic/claude-opus-5",
	"claude-opus-5-fast":     "anthropic/claude-opus-5-fast",
	"claude-opus-4.8":        "anthropic/claude-opus-4.8",
	"claude-sonnet-5":        "anthropic/claude-sonnet-5",
	"claude-haiku-4.5":       "anthropic/claude-haiku-4.5",
	"claude-sonnet-4.6":      "anthropic/claude-sonnet-4.6",
	"gpt":                    "openai/gpt-5.6-sol",
	"gpt-5":                  "openai/gpt-5.6-sol",
	"gpt-5.6-sol":            "openai/gpt-5.6-sol",
	"gpt-5.6-sol-pro":        "openai/gpt-5.6-sol-pro",
	"gpt-5.6-terra":          "openai/gpt-5.6-terra",
	"gpt-5.6-luna":           "openai/gpt-5.6-luna",
	"gpt-codex":              "openai/gpt-5.3-codex",
	"grok":                   "x-ai/grok-4.5",
	"grok-4.5":               "x-ai/grok-4.5",
	"grok-4.3":               "x-ai/grok-4.3",
	"grok-4.20":              "x-ai/grok-4.20",
	"gemini-pro":             "google/gemini-3.1-pro-preview",
	"gemini-flash":           "google/gemini-3.6-flash",
	"gemini-3.1-pro-preview": "google/gemini-3.1-pro-preview",
	"gemini-3.6-flash":       "google/gemini-3.6-flash",
	"gemini-3.5-flash":       "google/gemini-3.5-flash",
	"gemini-3.5-flash-lite":  "google/gemini-3.5-flash-lite",
}

var litellmModels = map[string]string{
	"claude-sonnet": "claude-sonnet-5",
	"claude-opus":   "claude-opus-5",
	"claude-haiku":  "claude-haiku-4-5",
	"claude-fable":  "claude-fable-5",
	"gpt":           "gpt-5.6-sol",
	"gpt-5.6-sol":   "gpt-5.6-sol",
	"gpt-5.6-terra": "gpt-5.6-terra",
	"grok":          "grok-4.5",
}

var localModels = map[string]string{
	"default":       "default",
	"claude-sonnet": "claude-sonnet-5",
	"gpt":           "gpt-5.6-sol",
	"grok":          "grok-4.5",
}

var customModels = map[string]string{
	"claude-sonnet": "claude-sonnet-5",
	"claude-opus":   "claude-opus-5",
	"claude-haiku":  "claude-haiku-4-5",
	"gpt":           "gpt-5.6-sol",
	"grok":          "grok-4.5",
}

// modelPolicies mirrors every CatalogProvider's model surface, keyed by
// provider id.
var modelPolicies = map[string]ModelPolicy{
	"openrouter": {
		SuggestModels: openrouterModels,
		RoleDefaults:  anthropicRoleDefaults,
		RoleFriendly: []string{
			"anthropic/claude-sonnet-5", "anthropic/claude-opus-5", "anthropic/claude-haiku-4.5",
			"anthropic/claude-fable-5", "anthropic/claude-opus-4.8",
			"openai/gpt-5.6-sol", "openai/gpt-5.6-terra", "openai/gpt-5.3-codex",
			"google/gemini-3.1-pro-preview", "google/gemini-3.6-flash", "x-ai/grok-4.5",
			"claude-sonnet-5", "claude-opus-5", "claude-haiku-4-5",
		},
		StaticFallback: []string{
			"anthropic/claude-sonnet-5", "anthropic/claude-opus-5", "anthropic/claude-haiku-4.5",
			"openai/gpt-5.6-sol", "google/gemini-3.1-pro-preview", "google/gemini-3.6-flash",
			"x-ai/grok-4.5",
		},
		LiveFetch: "openai",
	},
	"openai": {
		SuggestModels: openaiModels,
		RoleDefaults: map[string]string{
			"default": "gpt-5.6-sol", "sonnet": "gpt-5.6-sol",
			"opus": "gpt-5.6-sol", "haiku": "gpt-5.6-luna",
		},
		RoleFriendly:   []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.3-codex", "gpt-5.5", "gpt-4.1"},
		StaticFallback: []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.3-codex"},
		LiveFetch:      "openai",
	},
	"anthropic": {
		SuggestModels: anthropicModels,
		RoleDefaults:  anthropicRoleDefaults,
		RoleFriendly: []string{
			"claude-sonnet-5", "claude-opus-5", "claude-haiku-4-5",
			"claude-fable-5", "claude-opus-4-8", "claude-sonnet-4-6",
		},
		StaticFallback: []string{"claude-sonnet-5", "claude-opus-5", "claude-haiku-4-5"},
		LiveFetch:      "anthropic",
	},
	"grok-api": {
		SuggestModels: grokModels,
		RoleDefaults: map[string]string{
			"default": "grok-4.5", "sonnet": "grok-4.5", "opus": "grok-4.5", "haiku": "grok-4.3",
		},
		RoleFriendly: []string{"grok-4.5", "grok-4.3", "grok-4.20", "grok"},
		StaticFallback: []string{
			"grok-4.5", "grok-4.3", "grok-4.20-0309-reasoning",
		},
		LiveFetch: "openai",
	},
	"gemini-api": {
		SuggestModels: geminiModels,
		RoleDefaults: map[string]string{
			"default": "gemini-3.1-pro-preview", "sonnet": "gemini-3.6-flash",
			"opus": "gemini-3.1-pro-preview", "haiku": "gemini-3.6-flash",
		},
		RoleFriendly: []string{
			"gemini-3.1-pro-preview", "gemini-3.6-flash", "gemini-3.5-flash",
			"gemini-3.5-flash-lite", "gemini-3.1-flash-lite", "gemini-pro", "gemini-flash",
		},
		RoleModelHints: map[string][]string{
			"default": {"pro"}, "opus": {"pro"}, "sonnet": {"flash", "lite"}, "haiku": {"flash", "lite"},
		},
		StaticFallback: []string{},
		LiveFetch:      "gemini",
	},
	"litellm": {
		SuggestModels: litellmModels,
		RoleDefaults:  anthropicRoleDefaults,
		LiveFetch:     "openai",
	},
	"local": {
		SuggestModels: localModels,
		RoleDefaults:  anthropicRoleDefaults,
		LiveFetch:     "openai",
	},
	"custom": {
		SuggestModels: customModels,
		RoleDefaults:  anthropicRoleDefaults,
	},
	// opencode: Zen/Go platform keys speak OpenAI-compatible /models on
	// per-service bases (provider openCodeZenBaseURL/openCodeGoBaseURL).
	// Entries carry no endpoint and no vendor-published list, so there is
	// no static fallback: catalogs arrive via live fetch against the
	// entry's own base, warmed explicitly — never per snapshot tick.
	"opencode": {
		LiveFetch: "openai",
	},
}

// Policy returns the model policy for a catalog provider id, or nil when the
// provider declares none.
func Policy(providerID string) *ModelPolicy {
	if p, ok := modelPolicies[providerID]; ok {
		return &p
	}
	return nil
}

// AnthropicVersion exposes the pinned /models version header value.
func AnthropicVersion() string { return anthropicVersion }
