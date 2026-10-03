package transfer

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutatingMethodsTakeContext enforces the deadlock-trap invariant: every
// persisted mutation takes ctx as its first argument and has no ctx-less form.
// RestoreSelected is the case that motivated the rule — it locked a batch under
// provider scopes, so a context.Background() wrapper could not see the caller's
// locks and re-acquired them.
func TestMutatingMethodsTakeContext(t *testing.T) {
	testgate.CheckNoCtxlessMutations(t, ".", map[string]string{
		// No exceptions: every transfer mutation is ctx-first.
	})
}
