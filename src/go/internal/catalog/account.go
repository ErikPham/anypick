package catalog

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
	case "gemini", "google":
		return "gemini-api"
	case "grok", "xai":
		return "grok-api"
	case "opencode", "opencode-go", "opencode-zen", "zen":
		return "opencode"
	case "openrouter", "litellm", "local", "custom":
		return accountProviderID
	default:
		return ""
	}
}
