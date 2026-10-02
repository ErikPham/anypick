package journal

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/db"
)

func openJournal(t *testing.T) *OperationJournal {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &OperationJournal{DB: database.DB}
}

func TestJournalLifecycle(t *testing.T) {
	j := openJournal(t)

	entry, err := j.CreateContext(context.Background(), "activation", CreateOptions{
		AffectedResources: []string{"account/grok/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != StatePlanned {
		t.Fatalf("initial state = %s", entry.State)
	}

	got, err := j.Get(entry.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.AffectedResources) != 1 || got.AffectedResources[0] != "account/grok/work" {
		t.Fatalf("affected = %+v", got.AffectedResources)
	}

	next, err := j.UpdateContext(context.Background(), entry.ID, Entry{State: StateExecuting})
	if err != nil || next.State != StateExecuting {
		t.Fatalf("update: %v %s", err, next.State)
	}
	next, err = j.UpdateContext(context.Background(), entry.ID, Entry{State: StateCommitted})
	if err != nil || next.State != StateCommitted {
		t.Fatalf("commit update: %v %s", err, next.State)
	}

	incomplete, err := j.ListIncomplete()
	if err != nil || len(incomplete) != 0 {
		t.Fatalf("committed entry must not be incomplete: %+v %v", incomplete, err)
	}

	recent, err := j.ListRecent(10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent: %+v %v", recent, err)
	}
}

func TestIncompleteStates(t *testing.T) {
	j := openJournal(t)
	for _, state := range []State{StatePlanned, StateExecuting, StateVerifying, StateRollingBack} {
		if _, err := j.CreateContext(context.Background(), "op", CreateOptions{State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.CreateContext(context.Background(), "op", CreateOptions{State: StateFailed}); err != nil {
		t.Fatal(err)
	}
	incomplete, err := j.ListIncomplete()
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 4 {
		t.Fatalf("incomplete = %d, want 4", len(incomplete))
	}
}

func TestUpdateMissingEntry(t *testing.T) {
	j := openJournal(t)
	if _, err := j.UpdateContext(context.Background(), "nope", Entry{State: StateCommitted}); err == nil {
		t.Fatal("expected error for missing entry")
	}
}

// A stored column that no longer parses means the journal's record of what an
// operation touched is gone. Reading that as "touched nothing" would let
// recovery skip a half-applied mutation while reporting a clean start, so the
// entry must be marked instead.
func TestScanEntryMarksUnparsableStoredJSON(t *testing.T) {
	j := openJournal(t)
	ctx := context.Background()
	entry, err := j.CreateContext(ctx, "activation", CreateOptions{
		AffectedResources: []string{"account/workbuddy/main"},
		State:             StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.DB.ExecContext(ctx,
		`UPDATE operation_journal SET affected_resources_json = ? WHERE id = ?`,
		"{not-json", entry.ID); err != nil {
		t.Fatal(err)
	}

	got, err := j.GetContext(ctx, entry.ID)
	if err != nil {
		t.Fatalf("an unparsable column must not turn a read into a failure: %v", err)
	}
	if got == nil {
		t.Fatal("entry vanished")
	}
	if !got.Corrupt {
		t.Fatalf("entry = %+v, want Corrupt", got)
	}
}

func TestScanEntryTreatsAnEmptyColumnAsEmpty(t *testing.T) {
	// Rows written before a column existed hold "" or "null". That is not
	// corruption, and flagging it would make every legacy row unrecoverable.
	j := openJournal(t)
	ctx := context.Background()
	entry, err := j.CreateContext(ctx, "activation", CreateOptions{State: StateExecuting})
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{"", "null", "[]"} {
		if _, err := j.DB.ExecContext(ctx,
			`UPDATE operation_journal SET affected_resources_json = ?, backup_paths_json = ? WHERE id = ?`,
			stored, stored, entry.ID); err != nil {
			t.Fatal(err)
		}
		got, err := j.GetContext(ctx, entry.ID)
		if err != nil || got == nil {
			t.Fatalf("stored %q: %v", stored, err)
		}
		if got.Corrupt {
			t.Fatalf("stored %q: flagged as corrupt", stored)
		}
		if got.AffectedResources == nil || got.BackupPaths == nil {
			t.Fatalf("stored %q: lists must be non-nil: %+v", stored, got)
		}
	}
}
