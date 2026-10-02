package hub

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutatingMethodsTakeContext enforces the deadlock-trap invariant: every
// persisted mutation takes ctx as its first argument and has no ctx-less form.
// A wrapper that starts from context.Background() cannot see a coordinator
// lock its caller holds, so it re-acquires and blocks forever on the
// in-process mutex — the process hangs instead of failing. Reads
// (Get/List*) keep their ctx-less form because they take no lock.
func TestMutatingMethodsTakeContext(t *testing.T) {
	testgate.CheckNoCtxlessMutations(t, ".", map[string]string{
		// attemptWriter implements http.ResponseWriter, whose methods are named
		// Write/WriteHeader. They write a response, not persisted state, and
		// take no lock.
		"Write":       "http.ResponseWriter method; writes a response body, not state",
		"WriteHeader": "http.ResponseWriter method; writes a response status, not state",
	})
}
