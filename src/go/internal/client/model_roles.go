package client

import "strings"

// ModelRole is one configurable model slot the desktop UI may offer for a
// client. It mirrors ClientModelRole in src/clients/model-roles.ts.
//
// The list is deliberately *narrower* than the TypeScript tree: a role is
// published only when this runtime's adapter actually writes it. Offering a
// slot the adapter ignores would show a control that silently does nothing.
type ModelRole struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// claudeRoles mirrors CLAUDE_MODEL_ROLES. claude.Apply writes
// ANTHROPIC_MODEL plus the three tier env overrides from exactly these ids,
// so all four slots are real.
var claudeRoles = []ModelRole{
	{ID: "default", Label: "Default"},
	{ID: "sonnet", Label: "Sonnet"},
	{ID: "opus", Label: "Opus"},
	{ID: "haiku", Label: "Haiku"},
}

// defaultRoles mirrors DEFAULT_MODEL_ROLE — the vendor-neutral single slot.
var defaultRoles = []ModelRole{
	{ID: "default", Label: "Default"},
}

// RolesFor returns the model roles a client can be configured with.
//
// Known divergence from TypeScript (src/clients/model-roles.ts): TS gives
// codex CODEX_DESKTOP_MODEL_ROLES (default + list2…list5) because the desktop
// adapter exposes several independently editable slots. Go publishes the
// complete source catalog to Codex's model_catalog_json, but its native
// config has one active root `model` slot, so exposing list2…list5 here would
// offer controls that have no corresponding Go setting.
//
// Every other current adapter — codex, gemini, kiro, opencode, workbuddy,
// zcode, dsh, commandcode — consumes ModelRoles["default"] only.
func RolesFor(clientID string) []ModelRole {
	switch strings.ToLower(strings.TrimSpace(clientID)) {
	case "claude":
		return append([]ModelRole(nil), claudeRoles...)
	default:
		return append([]ModelRole(nil), defaultRoles...)
	}
}

// AllowedRoleIDs is the membership test used when applying a submitted role
// map: a role the client does not declare is rejected instead of being
// written into client config.
func AllowedRoleIDs(clientID string) map[string]bool {
	out := map[string]bool{}
	for _, role := range RolesFor(clientID) {
		out[role.ID] = true
	}
	return out
}
