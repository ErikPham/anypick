package proxy

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestLeaseMutationsTakeTheCoordinatorLock enforces ADR 0009 (CONC-01) and
// AGENTS.md invariant #5 for the proxy_leases table, which internal/db/tables.go
// assigns to this package.
//
// Before this gate, LeaseStore wrote rows with a raw ExecContext and took no
// lock at all, so two actors creating or deleting a lease for one provider
// interleave with nothing to order them. It was not caught because no gate in
// this package looked at locking: the ctx-first gate checks the signature, and
// Create/Delete were ctx-first while unlocked.
//
// The sweep is scoped to LeaseStore rather than the whole package. The rule is
// about the type that owns persisted state, and it is grounded in
// internal/db/tables.go:64. A service in this package cannot write SQL — it
// reaches the database through store.AccountStore's ctx-first mutators, which
// internal/store's own coordinator gate covers. Widening the sweep here would
// only flag every service method whose name reads as a mutation and turn the
// allowlist into a list of things nobody has judged (CODE_REVIEW.md §8).
func TestLeaseMutationsTakeTheCoordinatorLock(t *testing.T) {
	testgate.CheckUncoordinatedMutations(t, ".", []string{"LeaseStore"}, unguardedLeaseMutations)
}

// unguardedLeaseMutations is the escape hatch, and every entry carries the
// reason it is safe. The three entries below restate the reasoning at
// lease_store.go:33 in the form the gate can check; that comment is the
// authority and this list is its mirror, not a second opinion.
var unguardedLeaseMutations = map[string]string{
	"LeaseStore.HeartbeatContext":         "a liveness tick, not a structural change: the heartbeat goroutine's context is a long-lived cancel context that can never carry a provider lock, and giving it one per 15s tick would serialise every liveness write behind proxy cleanup and drop ticks on the 5s lock budget",
	"LeaseStore.HeartbeatInstanceContext": "the per-instance form of HeartbeatContext, with the same reason: an age stamp cannot change which rows exist in a way a concurrent structural change can observe, because both filter on identity and the writer re-checks",
	"LeaseStore.ReapStaleContext":         "a cross-provider sweep with no single scope, so the coordinator would only serialise reaps against reaps, which SQLite's single writer already does",
}
