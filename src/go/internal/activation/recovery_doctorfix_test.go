package activation

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/journal"
)

// TestRecoveryLeavesDoctorFixRowsAlone pins the exclusion: a doctor:fix journal
// row records a doctor repair, not an activation. Its affected resources are
// raw filesystem paths that no recovery branch recognises, so an executing row
// used to fall through to markFailed — every startup printed "incomplete
// operations could not be rolled back" for a self-referential non-operation,
// and complete_journal_rollback then planned a rollback of a rollback record.
// complete_journal_rollback consumes these rows deliberately; activation
// recovery must leave them alone.
func TestRecoveryLeavesDoctorFixRowsAlone(t *testing.T) {
	env := newTestEnv(t)
	j := env.Deps.Journal
	ctx := context.Background()

	entry, err := j.CreateContext(ctx, "doctor:fix", journal.CreateOptions{
		State:             journal.StateExecuting,
		AffectedResources: []string{env.Deps.Root + "/providers/grok/accounts/work/runtime/proxy.pid"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, rerr := RecoverIncompleteOperations(ctx, env.Deps)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if result.Recovered != 0 || len(result.Failed) != 0 || len(result.Refused) != 0 {
		t.Fatalf("recovery touched the doctor:fix row: %+v", result)
	}

	still, gerr := j.GetContext(ctx, entry.ID)
	if gerr != nil || still == nil || still.State != journal.StateExecuting {
		t.Fatalf("doctor:fix row state changed: %+v err=%v", still, gerr)
	}
}
