package store

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutatingMethodsTakeContext enforces the deadlock-trap invariant for the
// store package too: persisted mutations take ctx first and have no ctx-less
// form. Staging-dir helpers are exempt — they take no lock, so there is no
// re-entrancy to break; the commit point (WriteMeta) is guarded.
func TestMutatingMethodsTakeContext(t *testing.T) {
	testgate.CheckNoCtxlessMutations(t, ".", map[string]string{
		"PrepareSnapshot":         "creates a fresh staging dir only; no existing state is mutated and no lock is taken",
		"DiscardPreparedSnapshot": "removes only a staging dir this process created; no row or snapshot is touched and no lock is taken",
	})
}
