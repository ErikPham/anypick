package store

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutatingMethodsTakeTheCoordinatorLock enforces ADR 0009 (CONC-01) and
// AGENTS.md invariant #5: every persisted mutation runs under the coordinator.
//
// WithMutationLocks is opt-in at the call site, so without this test a new
// mutating method could be added that simply never takes the lock. It would
// pass review, pass the suite, and then corrupt a real credential file months
// later as a race nobody can reproduce. This turns that mistake into a build
// failure.
//
// The check is deliberately syntactic rather than behavioural: it answers "did
// this method route through the coordinator?", which is the property ADR 0009
// actually promises. The re-entrancy semantics are covered by internal/lock's
// own tests.
func TestMutatingMethodsTakeTheCoordinatorLock(t *testing.T) {
	testgate.CheckUncoordinatedMutations(t, ".", nil, unguardedMutations)
}

// mutatesByVerbs and takesCoordinatorLock both live in testgate now. This file
// used to keep its own shorter copy of each, so a verb or a wrapper added to
// the shared list reached the ctx-first gates and quietly missed this one —
// ReleaseProxyRuntimeContext and CopySnapshotFiles are persisted mutations, and
// deleting their s.mutate call passed while this gate stayed silent. One
// implementation, one place a verb has to be added.

// unguardedMutations is the escape hatch, and every entry must justify itself.
// Adding to this map is a decision, not a workaround — that is the point.
var unguardedMutations = map[string]string{
	// Creates a brand-new temp dir under the staging root and records it in
	// s.prepared behind s.mu. It mutates no existing row and no existing
	// snapshot, so there is nothing for another actor to race against. The
	// commit that matters is WriteMeta, which is guarded.
	"AccountStore.PrepareSnapshot": "creates a fresh staging dir only; no existing state is mutated (commit point is WriteMeta, which is guarded)",
	// Deletes a staging dir this process created, inside the staging root, and
	// is refused outside it. Same reasoning as PrepareSnapshot.
	"AccountStore.DiscardPreparedSnapshot": "removes only a staging dir this process created; no row or snapshot is touched",
	// The name reads as a mutation but the source of truth is only read:
	// SELECT path, content FROM account_snapshot_files, written into a staging
	// dir the caller owns. No row changes and the live snapshot is left alone
	// (ADR 0004), so there is no persisted state to serialise. The commit that
	// matters is the WriteMeta that follows, which is guarded.
	"AccountStore.CopySnapshotFiles": "reads committed snapshot blobs into a caller-owned staging dir; no row and no live snapshot is mutated",
}

// takesCoordinatorLock is gone too. It moved next to mutatesByVerbs in
// testgate, so a store cannot reintroduce a detector that silently accepts less
// than the shared one.
