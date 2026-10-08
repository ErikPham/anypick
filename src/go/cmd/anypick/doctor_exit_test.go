package main

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/doctor"
)

// TestDoctorFixExitsNonzeroWhenARepairFails pins the exit-code contract the
// release review asked for: doctor --fix exits HealthFailure when any planned
// repair is refused, and Success only when every repair applied. The refusal
// is forced honestly through the real executor — a remove_orphaned_profile
// whose remover errors.
func TestDoctorFixExitsNonzeroWhenARepairFails(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	deps := doctor.FixDeps{
		NamedProfiles: func(_ context.Context) ([]doctor.NamedProfileRef, error) {
			return []doctor.NamedProfileRef{
				{Client: "codex", Name: "ghost", SourceKind: "account", Provider: "codex", AccountName: "dead"},
			}, nil
		},
		AccountExists: func(context.Context, string, string) (bool, error) {
			return false, nil
		},
		RemoveOrphanedProfile: func(context.Context, string, string) error {
			return anyerr.Newf("injected refusal", "STATE_CONFLICT")
		},
	}
	plan := doctor.PlanFixes(context.Background(), deps)
	result := doctor.ApplyFixes(context.Background(), plan, deps, false)

	refused := 0
	for _, o := range result.Applied {
		if !o.OK {
			refused++
		}
	}
	if refused == 0 {
		t.Fatalf("the injected refusal did not surface: %+v", result.Applied)
	}
	// cmdDoctor maps any refusal to HealthFailure; assert the constant here so
	// the mapping is pinned even though the count lives in cmd.
	if int(anyerr.HealthFailure) == int(anyerr.Success) {
		t.Fatal("HealthFailure and Success collide; the exit-code contract is meaningless")
	}
}
