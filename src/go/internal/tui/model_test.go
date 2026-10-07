package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/outof0/anypick/go/internal/doctor"
)

// key builds a KeyMsg the way bubbletea delivers a single printable rune.
func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func special(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: t}
}

func updateModel(t *testing.T, m *Model, msg tea.Msg) (*Model, tea.Cmd) {
	t.Helper()
	got, cmd := m.Update(msg)
	model, ok := got.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", got)
	}
	return model, cmd
}

func TestQuitKeysExitFromTheHomeScreen(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"q", key('q')},
		{"ctrl+c", special(tea.KeyCtrlC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(context.Background(), nil)
			m.board = boardApps

			got, cmd := updateModel(t, m, tc.msg)

			if !got.quitting {
				t.Errorf("quitting = false, want true: %s must exit the TUI", tc.name)
			}
			if cmd == nil {
				t.Fatal("cmd = nil, want a quit command")
			}
			if msg := cmd(); msg == nil {
				t.Fatal("quit command produced no message")
			}
		})
	}
}

func TestQuitDoesNotFireFromASecondaryBoard(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false

	got, cmd := updateModel(t, m, key('q'))

	if got.quitting {
		t.Error("quitting = true, want false: a stray q inside a board must not drop the user out")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestCtrlCStillExitsFromAnyBoard(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts

	got, _ := updateModel(t, m, special(tea.KeyCtrlC))

	if !got.quitting {
		t.Error("quitting = false, want true: ctrl+c must always exit")
	}
}

func TestEscapeWalksBackToApps(t *testing.T) {
	m := New(context.Background(), nil)

	// From a drilled Accounts list, esc returns to the provider list.
	m.board = boardAccounts
	m.accountDrill = true
	if got, _ := updateModel(t, m, special(tea.KeyEsc)); got.accountDrill {
		t.Errorf("esc from drilled accounts left accountDrill = true, want false")
	}

	// From the provider list, esc returns to the Apps board.
	m.board = boardAccounts
	m.accountDrill = false
	got, _ := updateModel(t, m, special(tea.KeyEsc))
	if got.board != boardApps {
		t.Errorf("esc from accounts = %v, want apps", got.board)
	}
}

func TestHelpOpensAndCloses(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts

	got, _ := updateModel(t, m, key('?'))
	if !got.showHelp {
		t.Fatalf("? = showHelp %v, want true", got.showHelp)
	}

	// h is the same command: the docs once promised it and shipped it dead,
	// and the vim reflex is the point of having both spellings.
	got, _ = updateModel(t, m, key('h'))
	if !got.showHelp {
		t.Fatalf("h = showHelp %v, want true", got.showHelp)
	}

	got, _ = updateModel(t, got, special(tea.KeyEsc))
	if got.showHelp {
		t.Errorf("esc from help left showHelp = true, want false")
	}
}

func TestCursorStaysInsideTheList(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false
	m.providers = []ProviderRow{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	for i := 0; i < 5; i++ {
		m, _ = updateModel(t, m, key('j'))
	}
	if m.cursor != 2 {
		t.Errorf("cursor after 5x down = %d, want 2 (clamped)", m.cursor)
	}
	for i := 0; i < 5; i++ {
		m, _ = updateModel(t, m, key('k'))
	}
	if m.cursor != 0 {
		t.Errorf("cursor after 5x up = %d, want 0 (clamped)", m.cursor)
	}
}

func TestActionResultSetsFooterStatusAndReloads(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = true
	m.accountProvider = "codex"

	got, cmd := updateModel(t, m, actionMsg{text: "switched codex: a → b"})
	if got.status != "switched codex: a → b" {
		t.Errorf("status = %q", got.status)
	}
	if got.statusErr {
		t.Error("statusErr = true for a successful action")
	}
	if cmd == nil {
		t.Error("cmd = nil, want a reload of the current board")
	}

	failed, _ := updateModel(t, m, actionMsg{err: context.Canceled})
	if !failed.statusErr {
		t.Error("statusErr = false for a failed action")
	}
}

func TestConfirmGatesDestructiveAction(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = true
	m.accounts = []AccountRow{{ProviderID: "codex", Name: "work"}}

	got, _ := updateModel(t, m, key('d'))
	if got.confirm == nil {
		t.Fatal("delete without confirmation: confirm = nil")
	}

	// Declining must clear the prompt and leave the account alone.
	declined, _ := updateModel(t, got, key('n'))
	if declined.confirm != nil {
		t.Error("confirm still set after declining")
	}
	if declined.status != "cancelled" {
		t.Errorf("status = %q, want cancelled", declined.status)
	}
}

func TestDeleteIsNotOfferedWhenThereAreNoAccounts(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = true

	got, _ := updateModel(t, m, key('d'))
	if got.confirm != nil {
		t.Error("confirm = set with an empty account list")
	}
}

func TestProviderFilterNarrowsTheList(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false
	m.providers = []ProviderRow{
		{ID: "claude", DisplayName: "Claude"},
		{ID: "codex", DisplayName: "Codex"},
		{ID: "openai", DisplayName: "OpenAI"},
	}

	got, _ := updateModel(t, m, key('f'))
	if !got.filtering {
		t.Fatal("f did not enter filtering mode")
	}

	got, _ = updateModel(t, got, key('l'))
	if len(got.providers) != 1 || got.providers[0].ID != "claude" {
		t.Errorf("filter 'l' = %v, want [claude]", got.providers)
	}
	if got.filterText != "l" {
		t.Errorf("filterText = %q, want l", got.filterText)
	}

	// backspace re-widens the list.
	got, _ = updateModel(t, got, special(tea.KeyBackspace))
	if len(got.providers) != 3 {
		t.Errorf("after backspace providers = %v, want all 3", got.providers)
	}

	// esc leaves filtering and triggers a reload of the full list.
	left, cmd := updateModel(t, got, special(tea.KeyEsc))
	if left.filtering {
		t.Error("esc did not leave filtering mode")
	}
	if cmd == nil {
		t.Error("esc from filter should reload providers")
	}
}

func TestAddLoginTriggersSaveCommand(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false
	m.providers = []ProviderRow{{ID: "codex", DisplayName: "Codex"}}

	_, cmd := updateModel(t, m, key('a'))
	if cmd == nil {
		t.Fatal("a did not produce a save (add-login) command")
	}
}

func TestProviderDetailsShowsStatus(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false
	m.providers = []ProviderRow{{ID: "codex", DisplayName: "Codex", SavedCount: 3, Relation: RelationNoLive}}

	got, _ := updateModel(t, m, key('v'))
	if got.status == "" {
		t.Error("v produced no detail status for the provider")
	}
}

func TestExportProviderLaunchesCommand(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false
	m.providers = []ProviderRow{{ID: "codex", DisplayName: "Codex"}}

	_, cmd := updateModel(t, m, key('e'))
	if cmd == nil {
		t.Fatal("e did not produce an export command for the provider")
	}
}

func TestExportAccountLaunchesCommand(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = true
	m.accounts = []AccountRow{{ProviderID: "codex", Name: "work"}}

	_, cmd := updateModel(t, m, key('e'))
	if cmd == nil {
		t.Fatal("e did not produce an export command for the drilled account")
	}
}

func TestImportOpensPathPrompt(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = false
	m.providers = []ProviderRow{{ID: "codex", DisplayName: "Codex"}}

	opened, _ := updateModel(t, m, key('i'))
	if !opened.prompting {
		t.Fatal("i did not open the import path prompt")
	}
	if opened.promptRun == nil {
		t.Error("the import prompt has no run callback")
	}

	// Typing extends the path; esc cancels without running anything.
	typed, _ := updateModel(t, opened, key('a'))
	if typed.promptText != "a" {
		t.Errorf("promptText = %q, want a", typed.promptText)
	}
	cancelled, cmd := updateModel(t, typed, special(tea.KeyEsc))
	if cancelled.prompting {
		t.Error("esc did not cancel the prompt")
	}
	if cmd != nil {
		t.Error("esc from the prompt must not run a command")
	}

	// enter runs the prompt callback and clears prompting.
	_, run := updateModel(t, opened, special(tea.KeyEnter))
	if run == nil {
		t.Error("enter did not produce the import command")
	}
}

func TestDiagnoseLaunchesCommand(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardApps
	m.apps = []AppRow{{ClientID: "claude", DisplayName: "Claude"}}

	_, cmd := updateModel(t, m, key('d'))
	if cmd == nil {
		t.Fatal("d did not produce a diagnose command")
	}
}

func TestDiagnoseOverlayDismissesOnAnyKey(t *testing.T) {
	m := New(context.Background(), nil)
	m.diagnosis = &doctor.Report{ClientID: "claude", Status: doctor.StatusOK}

	got, cmd := updateModel(t, m, key('q'))
	if got.diagnosis != nil {
		t.Error("the diagnosis overlay was not dismissed by a key press")
	}
	if cmd != nil {
		t.Error("dismissing the diagnosis must not run a command")
	}
}

func TestAccountDetailsShowsStatus(t *testing.T) {
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = true
	m.accounts = []AccountRow{{
		ProviderID: "codex", Name: "work",
		Identity: "x@example.com", Active: true, ProxyRunning: true,
	}}

	got, _ := updateModel(t, m, key('v'))
	if got.status == "" {
		t.Error("v produced no detail status for the account")
	}
	if !strings.Contains(got.status, "active") || !strings.Contains(got.status, "proxy running") {
		t.Errorf("account detail = %q, want active + proxy running", got.status)
	}
}
