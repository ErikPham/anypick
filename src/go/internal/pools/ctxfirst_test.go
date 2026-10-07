package pools

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutatingMethodsTakeContext enforces the deadlock-trap invariant: every
// persisted mutation takes ctx as its first argument and has no ctx-less form.
// A wrapper that starts from context.Background() cannot see a coordinator
// lock its caller holds, so it re-acquires and blocks forever on the
// in-process mutex — the process hangs instead of failing. Reads
// (Get/List*) keep their ctx-less form for source compatibility, not because
// every read is lock-free: a store read that re-materializes a snapshot takes
// the provider lock, and a display path must use the cached read instead
// (AGENTS.md, the ctx-less mutation trap).
func TestMutatingMethodsTakeContext(t *testing.T) {
	testgate.CheckNoCtxlessMutations(t, ".", map[string]string{
		// No exceptions: all pools mutations are ctx-first.
	})
}
