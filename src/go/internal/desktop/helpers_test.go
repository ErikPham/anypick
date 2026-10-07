package desktop

import (
	"io"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
)

// newWaitedServer builds a server for a test that dispatches through handle,
// handleSafely or another handler method, and registers the Wait that
// server.go's Wait doc comment requires before the test's database closes. A
// handler runs its trailing catalog warm and snapshot refresh in a goroutine
// after the terminal frame, so a test that never waits lets that goroutine
// outlive t.TempDir() cleanup and the temp root fails to delete with
// "directory not empty".
func newWaitedServer(t *testing.T, deps Deps, in io.Reader, out io.Writer) *Server {
	t.Helper()
	srv := New(deps, in, out)
	t.Cleanup(srv.Wait)
	return srv
}

// newWaitedServerOut is newWaitedServer for the common case of a writer-only
// sink, so a call site reads as a drop-in for New(deps, strings.NewReader(""),
// out).
func newWaitedServerOut(t *testing.T, deps Deps, out io.Writer) *Server {
	t.Helper()
	return newWaitedServer(t, deps, strings.NewReader(""), out)
}

func TestLabelOr(t *testing.T) {
	if got := labelOr("My Label", "fallback"); got != "My Label" {
		t.Fatalf("labelOr=%q want My Label", got)
	}
	if got := labelOr("", "fallback"); got != "fallback" {
		t.Fatalf("labelOr empty=%q want fallback", got)
	}
}

func TestDetailFor(t *testing.T) {
	if got := detailFor(true, 4000); got != "running on port 4000" {
		t.Fatalf("detailFor running=%q", got)
	}
	if got := detailFor(false, 4000); got != "stopped" {
		t.Fatalf("detailFor stopped=%q", got)
	}
}

func TestTernary(t *testing.T) {
	if got := ternary(true, "a", "b"); got != "a" {
		t.Fatalf("ternary true=%q", got)
	}
	if got := ternary(false, "a", "b"); got != "b" {
		t.Fatalf("ternary false=%q", got)
	}
}

func TestDesignAppName(t *testing.T) {
	cases := map[string]string{
		"claude":      "Claude Code",
		"codex":       "Codex",
		"gemini":      "Gemini",
		"kiro":        "Kiro",
		"grok":        "Grok",
		"opencode":    "OpenCode",
		"commandcode": "Command Code",
		"custom":      "Custom",
	}
	for in, want := range cases {
		if got := designAppName(in); got != want {
			t.Errorf("designAppName(%q)=%q want %q", in, got, want)
		}
	}
	// Case-insensitive
	if got := designAppName("CLAUDE"); got != "Claude Code" {
		t.Fatalf("designAppName case-insensitive=%q", got)
	}
}

func TestPrimaryModelOf(t *testing.T) {
	// Explicit wins
	spec := bindings.Spec{Model: bindings.ExplicitModel("gpt-5")}
	if got := primaryModelOf(spec); got != "gpt-5" {
		t.Fatalf("explicit=%q", got)
	}
	// Omitted with modelRoles fallback
	spec2 := bindings.Spec{
		Model: bindings.OmittedModel(),
		ClientOptions: map[string]interface{}{
			"modelRoles": map[string]interface{}{"default": "claude-4"},
		},
	}
	if got := primaryModelOf(spec2); got != "claude-4" {
		t.Fatalf("modelRoles fallback=%q", got)
	}
	// Empty
	spec3 := bindings.Spec{Model: bindings.OmittedModel()}
	if got := primaryModelOf(spec3); got != "" {
		t.Fatalf("empty=%q want ''", got)
	}
	// Ref helper sanity (used in snapshot)
	if got := refs.DisplayRef(refs.AccountRef("grok", "work")); got != "grok/work" {
		t.Fatalf("DisplayRef=%q", got)
	}
}
