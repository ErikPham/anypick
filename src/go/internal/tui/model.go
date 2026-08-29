package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbletea"
	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/app"
)

// screen is the current view. The Go TUI keeps a flat enum plus an explicit
// back-target instead of the TypeScript tree's discriminated union, because
// the union carried both navigation data and a `back` pointer — here the data
// is reloaded from the services on entry, so only the back-target is state.
type screen int

const (
	screenProviders screen = iota
	screenAccounts
	screenDetail
	screenHelp
)

// Model is the bubbletea model. It holds no secrets: proxy tokens returned by
// StartProxy are discarded here by design (invariant #2).
type Model struct {
	app *app.App
	ctx context.Context

	screen screen
	// back is where Esc returns to from an overlay screen.
	back screen

	providers []ProviderRow
	accounts  []AccountRow

	// providerID is the provider whose accounts are on screen.
	providerID string
	// detail is the account index shown on screenDetail.
	detail int

	cursor int
	width  int
	height int

	// status is the most recent action result shown in the footer.
	status    string
	statusErr bool

	// confirm, when set, gates a destructive action behind y/n.
	confirm *confirmState
	// quitting reports that the program should exit on the next Update.
	quitting bool
}

type confirmState struct {
	question string
	run      func() tea.Msg
}

// New builds the initial model. Callers pass the context so that async service
// commands inherit the same cancellation as the command that launched the TUI.
func New(ctx context.Context, a *app.App) *Model {
	return &Model{app: a, ctx: ctx, screen: screenProviders, back: screenProviders}
}

// ── async result messages ────────────────────────────────────────

type providersLoadedMsg struct{ rows []ProviderRow }

type accountsLoadedMsg struct {
	rows []AccountRow
	err  error
}

// actionMsg is the outcome of any service call. It carries a human line and,
// separately, whether it was a failure — the footer styles them differently.
type actionMsg struct {
	text string
	err  error
}

// ── commands ─────────────────────────────────────────────────────

func loadProvidersCmd(ctx context.Context, a *app.App) tea.Cmd {
	return func() tea.Msg {
		return providersLoadedMsg{rows: LoadProviders(ctx, a)}
	}
}

func loadAccountsCmd(ctx context.Context, a *app.App, providerID string) tea.Cmd {
	return func() tea.Msg {
		rows, err := LoadAccounts(ctx, a, providerID)
		return accountsLoadedMsg{rows: rows, err: err}
	}
}

// useCmd switches the provider's live login to a saved account.
func useCmd(ctx context.Context, a *app.App, providerID, name string) tea.Cmd {
	return func() tea.Msg {
		res, err := a.Accounts.Use(ctx, providerID, name, accounts.UseOptions{})
		if err != nil {
			return actionMsg{err: err}
		}
		from := res.From
		if from == "" {
			from = "(none)"
		}
		return actionMsg{text: fmt.Sprintf("switched %s: %s → %s", providerID, from, res.To)}
	}
}

// saveLiveCmd saves the provider's current live login under a name derived
// from its identity. The name is only a suggestion, and force is always false:
// colliding with an existing account is an error, never an overwrite.
func saveLiveCmd(ctx context.Context, a *app.App, providerID string) tea.Cmd {
	return func() tea.Msg {
		cur, err := a.Accounts.Current(ctx, providerID)
		if err != nil {
			return actionMsg{err: err}
		}
		if cur == nil || !cur.Live.Present {
			return actionMsg{text: fmt.Sprintf(
				"no live login detected for %s — sign in with the official tool first", providerID)}
		}
		meta, err := a.Accounts.Save(ctx, providerID, suggestAccountName(cur.Live.Identity), false)
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{text: fmt.Sprintf("saved %s/%s", providerID, meta.Name)}
	}
}

// deleteCmd removes a saved account. Destructive, so it is only reachable
// through a confirmState.
func deleteCmd(ctx context.Context, a *app.App, providerID, name string) tea.Cmd {
	return func() tea.Msg {
		if err := a.Accounts.Delete(ctx, providerID, name); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{text: fmt.Sprintf("deleted %s/%s", providerID, name)}
	}
}

// toggleProxyCmd starts or stops the account's proxy. The token in StartResult
// is intentionally unused.
func toggleProxyCmd(ctx context.Context, a *app.App, providerID, name string, running bool) tea.Cmd {
	return func() tea.Msg {
		if running {
			if err := a.Proxy.Stop(ctx, providerID, name); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{text: fmt.Sprintf("stopped proxy for %s/%s", providerID, name)}
		}
		res, err := a.Proxy.StartProxy(ctx, providerID, name)
		if err != nil {
			return actionMsg{err: err}
		}
		// res.Token is deliberately dropped: the footer shows the endpoint only.
		return actionMsg{text: fmt.Sprintf("proxy listening on %s", res.Endpoint)}
	}
}

// ── bubbletea interface ──────────────────────────────────────────

func (m *Model) Init() tea.Cmd {
	return loadProvidersCmd(m.ctx, m.app)
}

// listLen reports how many rows the current screen can move the cursor over.
func (m *Model) listLen() int {
	switch m.screen {
	case screenProviders:
		return len(m.providers)
	case screenAccounts, screenDetail:
		return len(m.accounts)
	default:
		return 0
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.confirm != nil {
			return m.updateConfirm(msg)
		}
		return m.updateKey(msg)

	case providersLoadedMsg:
		m.providers = msg.rows
		if m.cursor >= len(m.providers) {
			m.cursor = max(0, len(m.providers)-1)
		}
		return m, nil

	case accountsLoadedMsg:
		if msg.err != nil {
			m.statusErr = true
			m.status = msg.err.Error()
			m.accounts = nil
			return m, nil
		}
		m.accounts = msg.rows
		m.cursor = 0
		return m, nil

	case actionMsg:
		m.statusErr = msg.err != nil
		if msg.err != nil {
			m.status = msg.err.Error()
		} else {
			m.status = msg.text
		}
		// Any mutation invalidates every cached row.
		switch m.screen {
		case screenProviders:
			return m, loadProvidersCmd(m.ctx, m.app)
		case screenAccounts, screenDetail:
			return m, loadAccountsCmd(m.ctx, m.app, m.providerID)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		run := m.confirm.run
		m.confirm = nil
		return m, run
	case "n", "N", "esc", "q":
		m.confirm = nil
		m.status = "cancelled"
		m.statusErr = false
		return m, nil
	}
	return m, nil
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "ctrl+c", "q":
		// q only quits from the home screen; deeper screens use it for nothing,
		// so a stray q cannot drop the user out of a provider they navigated into.
		if key == "ctrl+c" || m.screen == screenProviders || m.screen == screenHelp {
			m.quitting = true
			return m, tea.Quit
		}
	case "?":
		if m.screen != screenHelp {
			m.back = m.screen
			m.screen = screenHelp
		}
		return m, nil
	case "esc":
		switch m.screen {
		case screenHelp:
			m.screen = m.back
			return m, nil
		case screenDetail:
			m.screen = screenAccounts
			return m, nil
		case screenAccounts:
			m.screen = screenProviders
			m.cursor = 0
			return m, loadProvidersCmd(m.ctx, m.app)
		}
		return m, nil
	case "r":
		switch m.screen {
		case screenProviders:
			return m, loadProvidersCmd(m.ctx, m.app)
		case screenAccounts, screenDetail:
			return m, loadAccountsCmd(m.ctx, m.app, m.providerID)
		}
		return m, nil
	}

	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < m.listLen()-1 {
			m.cursor++
		}
		return m, nil
	case "enter", "l", "right":
		return m.activate()
	}

	switch m.screen {
	case screenAccounts:
		return m.accountsKey(key)
	}
	return m, nil
}

func (m *Model) accountsKey(key string) (tea.Model, tea.Cmd) {
	if len(m.accounts) == 0 || m.cursor >= len(m.accounts) {
		return m, nil
	}
	row := m.accounts[m.cursor]
	switch key {
	case "u":
		return m, useCmd(m.ctx, m.app, row.ProviderID, row.Name)
	case "p":
		return m, toggleProxyCmd(m.ctx, m.app, row.ProviderID, row.Name, row.ProxyRunning)
	case "d":
		m.confirm = &confirmState{
			question: fmt.Sprintf("Delete saved account %s/%s?", row.ProviderID, row.Name),
			run:      deleteCmd(m.ctx, m.app, row.ProviderID, row.Name),
		}
		return m, nil
	case "s":
		return m, saveLiveCmd(m.ctx, m.app, row.ProviderID)
	}
	return m, nil
}

// activate drills into the row under the cursor.
func (m *Model) activate() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenProviders:
		if m.cursor >= len(m.providers) {
			return m, nil
		}
		m.providerID = m.providers[m.cursor].ID
		m.screen = screenAccounts
		m.cursor = 0
		m.status = ""
		m.statusErr = false
		return m, loadAccountsCmd(m.ctx, m.app, m.providerID)
	case screenAccounts:
		if m.cursor >= len(m.accounts) {
			return m, nil
		}
		m.detail = m.cursor
		m.screen = screenDetail
		return m, nil
	}
	return m, nil
}
