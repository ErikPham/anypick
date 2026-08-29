package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
			m.screen = screenProviders

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

func TestQuitDoesNotFireFromAScreenTheUserNavigatedInto(t *testing.T) {
	m := New(context.Background(), nil)
	m.screen = screenAccounts
	m.providerID = "codex"

	got, cmd := updateModel(t, m, key('q'))

	if got.quitting {
		t.Error("quitting = true, want false: a stray q inside a provider must not drop the user out")
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

func TestCtrlCStillExitsFromAnyScreen(t *testing.T) {
	m := New(context.Background(), nil)
	m.screen = screenAccounts

	got, _ := updateModel(t, m, special(tea.KeyCtrlC))

	if !got.quitting {
		t.Error("quitting = false, want true: ctrl+c must always exit")
	}
}

func TestEscapeWalksBackUpTheScreenStack(t *testing.T) {
	m := New(context.Background(), nil)

	m.screen = screenDetail
	if got, _ := updateModel(t, m, special(tea.KeyEsc)); got.screen != screenAccounts {
		t.Errorf("esc from detail = %v, want accounts", got.screen)
	}

	m.screen = screenAccounts
	got, _ := updateModel(t, m, special(tea.KeyEsc))
	if got.screen != screenProviders {
		t.Errorf("esc from accounts = %v, want providers", got.screen)
	}
}

func TestHelpOpensAndReturnsToWhereItWasOpenedFrom(t *testing.T) {
	m := New(context.Background(), nil)
	m.screen = screenAccounts

	got, _ := updateModel(t, m, key('?'))
	if got.screen != screenHelp {
		t.Fatalf("? = %v, want help", got.screen)
	}
	if got.back != screenAccounts {
		t.Errorf("back = %v, want accounts so esc returns the user", got.back)
	}

	got, _ = updateModel(t, got, special(tea.KeyEsc))
	if got.screen != screenAccounts {
		t.Errorf("esc from help = %v, want accounts", got.screen)
	}
}

func TestCursorStaysInsideTheList(t *testing.T) {
	m := New(context.Background(), nil)
	m.screen = screenProviders
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
	m.screen = screenAccounts
	m.providerID = "codex"

	got, cmd := updateModel(t, m, actionMsg{text: "switched codex: a → b"})
	if got.status != "switched codex: a → b" {
		t.Errorf("status = %q", got.status)
	}
	if got.statusErr {
		t.Error("statusErr = true for a successful action")
	}
	if cmd == nil {
		t.Error("cmd = nil, want a reload of the current screen")
	}

	failed, _ := updateModel(t, m, actionMsg{err: context.Canceled})
	if !failed.statusErr {
		t.Error("statusErr = false for a failed action")
	}
}

func TestConfirmGatesDestructiveAction(t *testing.T) {
	m := New(context.Background(), nil)
	m.screen = screenAccounts
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
	m.screen = screenAccounts

	got, _ := updateModel(t, m, key('d'))
	if got.confirm != nil {
		t.Error("confirm = set with an empty account list")
	}
}
