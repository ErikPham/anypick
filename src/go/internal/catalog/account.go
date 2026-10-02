package catalog

import "strings"

// CatalogProviderFor maps a saved-account provider id to the catalog policy
// describing its models. First-party logins share their API flavor's
// vendor-published list (a Claude login serves Claude ids); gateway flavors
// pass through unchanged. Unknown ids deliberately map to "": an empty
// catalog is honest, a guessed one would publish models the account cannot
// serve and manufacture hub conflicts out of nothing.
func CatalogProviderFor(accountProviderID string) string {
	switch accountProviderID {
	case "claude", "anthropic":
		return "anthropic"
	case "codex", "openai":
		return "openai"
	case "gemini", "google", "gemini-api":
		return "gemini-api"
	case "grok", "xai", "grok-api":
		return "grok-api"
	case "opencode", "opencode-go", "opencode-zen", "zen":
		return "opencode"
	case "openrouter", "litellm", "local", "custom":
		return accountProviderID
	// Compatible providers keep their own catalog identity. Their resolved
	// upstream supplies live discovery, so this is a cache namespace rather
	// than a shipped model list.
	case "deepseek", "kimi", "zai", "qwen", "groq", "mistral", "together",
		"mimo", "minimax", "doubao", "siliconflow", "stepfun", "hunyuan",
		"modelscope", "ark", "tokenhub":
		return accountProviderID
	default:
		return ""
	}
}

// CatalogIdentityFor returns the cache namespace used by live discovery. A
// provider with a built-in model policy is normalized to that policy (for
// example codex → openai); a provider without one keeps its own id so a
// provider-owned live catalog can still be cached without an app release.
// Unknown ids are intentionally not given a static policy by this helper.
func CatalogIdentityFor(providerID string) string {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return ""
	}
	if catalogID := CatalogProviderFor(providerID); catalogID != "" {
		return catalogID
	}
	return providerID
}

// AccountCatalogKey is the persisted identity of one saved account's live
// model catalog. The provider alone is not an identity: two accounts can share
// an origin and still expose different entitlements, so the account name is
// part of the key. It is a cache identity only and is never dialed.
// internal/proxy names the same shape as a provider-local catalog scope.
func AccountCatalogKey(providerID, accountName string) string {
	return "account/" + providerID + "/" + accountName
}
