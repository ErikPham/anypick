package journal

import (
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

	entry, err := j.Create("activation", CreateOptions{
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

	next, err := j.Update(entry.ID, Entry{State: StateExecuting})
	if err != nil || next.State != StateExecuting {
		t.Fatalf("update: %v %s", err, next.State)
	}
	next, err = j.Update(entry.ID, Entry{State: StateCommitted})
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
		if _, err := j.Create("op", CreateOptions{State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Create("op", CreateOptions{State: StateFailed}); err != nil {
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
	if _, err := j.Update("nope", Entry{State: StateCommitted}); err == nil {
		t.Fatal("expected error for missing entry")
	}
}
