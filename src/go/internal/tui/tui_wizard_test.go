package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
)

// TestWizardPlansGlobalBinding drives the route-build wizard through Update
// (no TTY needed) and asserts the pure planner produces a persistent
// global-binding plan for the chosen app. PlanActivation is side-effect free
// (invariant #3), so this exercises the wizard → engine wiring without
// spawning proxies or touching live client config.
func TestWizardPlansGlobalBinding(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ANYPICK_HOME", tmp)
	t.Setenv("HOME", tmp)

	a, cleanup, err := app.Ready(context.Background(), app.Options{SkipMigrate: true})
	if err != nil {
		t.Fatalf("app.Ready: %v", err)
	}
	defer cleanup()

	// Seed a gateway claude can actually route through. claude speaks the
	// Anthropic protocol, so the gateway must expose an Anthropic surface
	// (openrouter's https://openrouter.ai/api); a bare openai gateway would be
	// rejected with "unsupported transport", exactly as the CLI would reject it.
	if _, err := a.Runtime.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta: profile.Meta{
			Name:         "test-gw",
			Provider:     "openrouter",
			Endpoint:     "https://openrouter.ai/api",
			DefaultModel: "gpt-4o",
		},
	}); err != nil {
		t.Fatalf("seed gateway: %v", err)
	}

	m := New(context.Background(), a)
	m = applyCmd(t, m, m.Init())

	if len(m.apps) == 0 {
		t.Fatal("Apps board loaded no routing apps")
	}
	ci := indexOfApp(m.apps, "claude")
	if ci < 0 {
		t.Fatal("claude not present on Apps board")
	}
	m.cursor = ci

	// enter → open the wizard → load source options.
	m, cmd := updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = applyCmd(t, m, cmd)
	if m.wizard == nil {
		t.Fatal("wizard did not open on enter")
	}
	if len(m.wizard.sources) == 0 {
		t.Fatal("wizard offered no sources despite a seeded gateway")
	}

	// Choose the gateway source.
	m.cursor = indexOfSource(m.wizard.sources, refs.KindGateway, "test-gw")
	m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.wizard.step != wizardModels {
		t.Fatalf("expected models step after choosing a gateway, got %v", m.wizard.step)
	}

	// Edit the default model: enter to start editing, type, enter to commit.
	m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	for _, r := range "gpt-4o-mini" {
		m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// 'a' plans the route (preview).
	m, cmd = updateModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = applyCmd(t, m, cmd)

	if m.wizard.planErr != "" {
		t.Fatalf("planning failed: %s", m.wizard.planErr)
	}
	plan := m.wizard.plan
	if plan == nil {
		t.Fatal("planner returned no plan")
	}
	if plan.Client != "claude" {
		t.Errorf("plan.Client = %q, want claude", plan.Client)
	}
	if plan.Mode != activation.ModePersistent {
		t.Errorf("plan.Mode = %q, want persistent (global binding)", plan.Mode)
	}
	if plan.ResolvedSource == nil || plan.ResolvedSource.Ref.Kind != refs.KindGateway {
		t.Errorf("expected resolved gateway source, got %+v", plan.ResolvedSource)
	}
	commit := false
	for _, s := range plan.Steps {
		if s.Kind == activation.StepCommitGlobalBinding {
			commit = true
		}
	}
	if !commit {
		t.Errorf("plan has no CommitGlobalBinding step: %v", plan.Steps)
	}

	// esc unwinds one step: preview -> models (not straight to source), so the
	// user can still tweak the model before backing out to the source.
	m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.wizard.step != wizardModels {
		t.Errorf("esc from preview should return to models, got %v", m.wizard.step)
	}
	// a second esc returns to the source step.
	m, _ = updateModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.wizard.step != wizardSource {
		t.Errorf("esc from models should return to source, got %v", m.wizard.step)
	}
}

// applyCmd runs a bubbletea command and feeds its message back into Update, so
// a test can advance the model the same way the runtime loop does.
func applyCmd(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	nm, _ := m.Update(msg)
	return nm.(*Model)
}

func indexOfApp(apps []AppRow, clientID string) int {
	for i, r := range apps {
		if r.ClientID == clientID {
			return i
		}
	}
	return -1
}

func indexOfSource(opts []SourceOption, kind refs.Kind, name string) int {
	for i, o := range opts {
		if o.Ref.Kind == kind && o.Ref.Name == name {
			return i
		}
	}
	return -1
}
