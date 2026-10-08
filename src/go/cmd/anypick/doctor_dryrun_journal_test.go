package main

import (
	"context"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/journal"
)

// TestDoctorDryRunLeavesJournalUntouched proves the dry-run contract at the
// command boundary, not just inside PlanFixes. Startup maintenance ran BEFORE
// the doctor parsed its flags, so a dry run printed its plan over a tree the
// recovery had already rewritten; maintenance now skips on every dry run, and
// this test holds that.
func TestDoctorDryRunLeavesJournalUntouched(t *testing.T) {
	env := isolateEnv(t)
	_ = env

	// Seed an executing activation entry with maintenance DISABLED (the way a
	// crashed run would have left it). Seeding through appReadyForTest would
	// let maintenance mark it failed before the dry run ever started, which is
	// exactly the defect this test guards against.
	ctx := context.Background()
	a, closeFn, err := app.Ready(ctx, app.Options{SkipMigrate: true, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	j := &journal.OperationJournal{DB: a.DB.DB}
	entry, cerr := j.CreateContext(ctx, "activate", journal.CreateOptions{
		State: journal.StateExecuting, AffectedResources: []string{"client/claude"},
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	closeFn()

	// appReadyForTest itself runs maintenance unless skipped — the entry
	// survived that, which is the precondition. Now the dry run: maintenance
	// must be skipped so the entry is still executing afterwards.
	exit := 0
	var stdout, stderr string
	stdout, stderr = captureBoth(t, func() {
		exit = run([]string{"doctor", "--fix", "--dry-run"})
	})
	if exit != int(anyerr.Success) {
		t.Fatalf("doctor --fix --dry-run exited %d: %s", exit, stderr)
	}
	if !strings.Contains(stdout, "dry-run") && !strings.Contains(stderr, "dry-run") {
		t.Fatalf("dry run printed no plan: stdout=%q stderr=%q", stdout, stderr)
	}

	a2, closeFn2, err := app.Ready(ctx, app.Options{SkipMigrate: true, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn2()
	j2 := &journal.OperationJournal{DB: a2.DB.DB}
	still, gerr := j2.GetContext(ctx, entry.ID)
	if gerr != nil || still == nil || still.State != journal.StateExecuting {
		t.Fatalf("the dry run mutated the journal: %+v err=%v", still, gerr)
	}
}
