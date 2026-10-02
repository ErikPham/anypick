package desktop

import (
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
)

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
