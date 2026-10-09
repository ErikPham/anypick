package client

import "strings"

// ModelRole is one configurable model slot the desktop UI may offer for a
// client.
//
// The list is deliberately *narrow*: a role is published only when the
// adapter actually writes it. Offering a
// slot the adapter ignores would show a control that silently does nothing.
type ModelRole struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// The four claude slots are all real: claude.Apply writes ANTHROPIC_MODEL
// plus the three tier env overrides from exactly these ids.
var claudeRoles = []ModelRole{
	{ID: "default", Label: "Default"},
	{ID: "sonnet", Label: "Sonnet"},
	{ID: "opus", Label: "Opus"},
	{ID: "haiku", Label: "Haiku"},
}

// defaultRoles is the vendor-neutral single slot.
var defaultRoles = []ModelRole{
	{ID: "default", Label: "Default"},
}

// RolesFor returns the model roles a client can be configured with.
//
// Only claude publishes multiple slots: the desktop adapter supports several
// independently editable slots, but Go publishes the complete source catalog
// to Codex's model_catalog_json while its native config has one active root
// `model` slot, so exposing list2…list5 here would offer controls that have
// no corresponding setting.
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
