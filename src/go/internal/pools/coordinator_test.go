package pools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutatingMethodsTakeTheCoordinatorLock enforces ADR 0009 (CONC-01) and
// AGENTS.md invariant #5: every persisted mutation runs under the coordinator.
//
// The check lives in internal/testgate. This package used to carry a private
// mutatesByVerbs list, and it had drifted: Attach, Detach, Migrate, Restore,
// Record, Release, Copy, Heartbeat and Reap were all missing, so a Store method
// named with one of those verbs could lose its lock while this gate stayed
// green. MutationVerbs is the detection's entire signal; a second copy is a
// second answer to the same question (CODE_REVIEW.md §5).
func TestMutatingMethodsTakeTheCoordinatorLock(t *testing.T) {
	testgate.CheckUncoordinatedMutations(t, ".", []string{"Store"}, unguardedMutations)
}

// TestGateCatchesANewlySharedVerb pins the reason the private copy was deleted.
// "Release" is in testgate.MutationVerbs because ReleaseProxyRuntimeContext is a
// persisted mutation that self-locks today; the copy this package carried never
// gained the verb, so deleting that lock would have passed a green gate.
// ReleaseThing is exactly the method the copy could not see. The other two
// coordinator gates cover Attach and Heartbeat, so the three cases together
// pin three of the nine verbs the copies were missing.
func TestGateCatchesANewlySharedVerb(t *testing.T) {
	dir := t.TempDir()
	source := "package pools\n\ntype Store struct{}\n\nfunc (s *Store) ReleaseThing() error { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "store.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := testgate.UncoordinatedMutations(dir, []string{"Store"}, nil)
	if err != nil {
		t.Fatalf("coordinator gate could not run: %v", err)
	}
	if len(findings) != 1 || findings[0].Method != "Store.ReleaseThing" {
		t.Fatalf("findings = %v, want the unlocked Store.ReleaseThing mutator", findings)
	}
}

// unguardedMutations is the escape hatch, and every entry must justify itself.
// Adding to this map is a decision, not a workaround — that is the point.
var unguardedMutations = map[string]string{}
