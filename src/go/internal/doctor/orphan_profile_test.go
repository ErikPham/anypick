package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestPlanAndRemoveOrphanedProfiles pins the consented path: the plan names
// only profiles whose account is GONE (a lookup error plans nothing, fail
// closed), and applying removes the profile ROW — never a credential home.
// The startup sweep that used to do this unattended now reports only.
func TestPlanAndRemoveOrphanedProfiles(t *testing.T) {
	var removed []string
	deps := FixDeps{
		NamedProfiles: func(context.Context) ([]NamedProfileRef, error) {
			return []NamedProfileRef{
				{Client: "codex", Name: "alive", SourceKind: "account", Provider: "codex", AccountName: "live"},
				{Client: "codex", Name: "gone", SourceKind: "account", Provider: "codex", AccountName: "dead"},
				{Client: "codex", Name: "gatewayy", SourceKind: "gateway", Provider: "", AccountName: ""},
			}, nil
		},
		AccountExists: func(_ context.Context, provider, name string) (bool, error) {
			if name == "dead" {
				return false, nil
			}
			if name == "boom" {
				return false, errors.New("lock timeout")
			}
			return true, nil
		},
		RemoveOrphanedProfile: func(_ context.Context, client, name string) error {
			removed = append(removed, client+"/"+name)
			return nil
		},
	}
	ctx := context.Background()

	plan := PlanFixes(ctx, deps)
	var ids []string
	for _, a := range plan.Actions {
		if a.Kind == FixRemoveOrphanedProfile {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) != 1 || ids[0] != "remove_orphaned_profile:codex/gone" {
		t.Fatalf("plan %v, want exactly remove_orphaned_profile:codex/gone", ids)
	}

	ApplyFixes(ctx, plan, deps, false)
	if len(removed) != 1 || removed[0] != "codex/gone" {
		t.Fatalf("removed %v, want [codex/gone]", removed)
	}

	// Fail closed: a lookup error plans nothing for that profile.
	deps.AccountExists = func(_ context.Context, _, _ string) (bool, error) {
		return false, errors.New("lock timeout")
	}
	plan = PlanFixes(ctx, deps)
	for _, a := range plan.Actions {
		if a.Kind == FixRemoveOrphanedProfile {
			t.Fatalf("a lookup error planned a removal: %s", a.ID)
		}
	}
	_ = strings.TrimSpace
}
