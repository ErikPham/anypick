package tui

import (
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestPlanAndActionErrorsKeepSuggestions(t *testing.T) {
	err := anyerr.Newf("plan failed", "PLAN_FAILED", anyerr.WithSuggestions("anypick doctor"))
	m := Model{wizard: &wizardState{}}
	next, _ := m.Update(planReadyMsg{err: err})
	got := next.(*Model)
	view := strings.Join(got.viewWizardPreview(), "\n")
	if !strings.Contains(view, "plan failed") || !strings.Contains(view, "anypick doctor") {
		t.Fatalf("preview dropped the suggestion: %s", view)
	}

	next, _ = got.Update(actionMsg{err: err})
	got = next.(*Model)
	if got.statusCause == nil {
		t.Fatal("action error was dropped")
	}
	if !strings.Contains(got.status, "anypick doctor") {
		t.Fatalf("status = %q", got.status)
	}
}
