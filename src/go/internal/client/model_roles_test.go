package client

import "testing"

// Multi-role clients, by id. Every other built-in client is expected to
// declare the single default slot; adding a client without classifying it
// here fails the build, the same way internal/transport/adapter_test.go
// refuses an unclassified client row.
var wantClaudeRoles = []ModelRole{
	{ID: "default", Label: "Default"},
	{ID: "sonnet", Label: "Sonnet"},
	{ID: "opus", Label: "Opus"},
	{ID: "haiku", Label: "Haiku"},
}

// defaultOnlyClients are the built-ins whose adapters consume
// ModelRoles["default"] and nothing else.
var defaultOnlyClients = map[string]bool{
	"codex": true, "gemini": true, "kiro": true, "opencode": true,
	"workbuddy": true, "zcode": true, "dsh": true, "commandcode": true,
	"pi": true, "omp": true,
}

func TestRolesForCoversEveryRegisteredClient(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterBuiltin(reg, t.TempDir()); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	ids := reg.IDs()
	if len(ids) == 0 {
		t.Fatal("no registered clients")
	}
	for _, id := range ids {
		if id == "claude" || defaultOnlyClients[id] {
			continue
		}
		t.Errorf(
			"client %q has no explicit model-role expectation: "+
				"classify it in model_roles.go and in this test", id)
	}
	for id := range defaultOnlyClients {
		if _, err := reg.Get(id); err != nil {
			t.Errorf("stale expectation for client %q: %v", id, err)
		}
	}
}

func TestRolesForClaudeExposesTheFourEnvSlots(t *testing.T) {
	got := RolesFor("claude")
	if len(got) != len(wantClaudeRoles) {
		t.Fatalf("RolesFor(claude) = %#v want %#v", got, wantClaudeRoles)
	}
	for i := range wantClaudeRoles {
		if got[i] != wantClaudeRoles[i] {
			t.Errorf("role %d = %#v want %#v", i, got[i], wantClaudeRoles[i])
		}
	}
}

func TestRolesForDefaultsToOneSlot(t *testing.T) {
	for _, id := range []string{"", "  ", "codex", "gemini", "kiro", "opencode", "workbuddy", "zcode", "dsh", "commandcode", "pi", "omp", "plugin-x"} {
		got := RolesFor(id)
		if len(got) != 1 || got[0].ID != "default" {
			t.Errorf("RolesFor(%q) = %#v want exactly the default slot", id, got)
		}
	}
}

// The default slot must exist and come first: the UI treats role[0] as the
// required selection and activation rejects a role map without "default".
func TestRolesForAlwaysStartsWithDefault(t *testing.T) {
	for _, id := range []string{"claude", "codex", "unknown"} {
		roles := RolesFor(id)
		if len(roles) == 0 || roles[0].ID != "default" {
			t.Fatalf("RolesFor(%q) = %#v want default first", id, roles)
		}
	}
}

func TestRolesForReturnsACopy(t *testing.T) {
	first := RolesFor("claude")
	first[0].Label = "mutated"
	if again := RolesFor("claude"); again[0].Label == "mutated" {
		t.Fatal("RolesFor returned the shared slice; a caller mutated package state")
	}
}

func TestAllowedRoleIDsRejectsForeignRoles(t *testing.T) {
	allowed := AllowedRoleIDs("codex")
	if !allowed["default"] {
		t.Fatal("codex must allow the default role")
	}
	if allowed["haiku"] {
		t.Fatal("codex must not allow a Claude-only role")
	}
}
