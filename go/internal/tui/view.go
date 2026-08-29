package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Styles. Adaptive colors keep the TUI readable on both light and dark
// terminals instead of assuming the developer's theme.
var (
	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "235", Dark: "252"})

	styleHeader = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "245"})

	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "27", Dark: "39"})

	styleDim = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"})

	styleOK = lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"})

	styleWarn = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"})

	styleError = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "203"})

	styleFooter = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "245"})
)

// View renders the whole screen.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	width := m.width
	if width == 0 {
		width = 80
	}

	var body []string
	var title string
	var hints string

	switch m.screen {
	case screenProviders:
		title = "Providers"
		body = m.viewProviders()
		hints = "↑↓ move · ⏎ open · r reload · ? help · q quit"
	case screenAccounts:
		title = "Accounts · " + m.providerID
		body = m.viewAccounts()
		hints = "↑↓ move · ⏎ detail · u use · p proxy · d delete · s save live · esc back"
	case screenDetail:
		title = "Account detail"
		body = m.viewDetail()
		hints = "esc back · ? help"
	case screenHelp:
		title = "Help"
		body = m.viewHelp()
		hints = "esc back"
	}

	out := styleTitle.Render(title)
	if m.screen == screenProviders {
		out += "  " + styleHeader.Render(providerSummary(m.providers))
	}
	out += "\n\n"

	visible := m.visibleRows(body)
	if len(body) == 0 {
		out += styleDim.Render("  (nothing to show)") + "\n"
	} else {
		out += strings.Join(visible, "\n") + "\n"
	}

	out += "\n" + styleFooter.Render(hints) + "\n"
	out += m.footerStatus()

	if m.confirm != nil {
		out += "\n" + styleWarn.Render(m.confirm.question+" (y/n)") + "\n"
	}
	return out
}

// visibleRows windows the body so a long list cannot push the footer off a
// short terminal. The cursor is kept in view.
func (m *Model) visibleRows(body []string) []string {
	if len(body) == 0 {
		return body
	}
	reserve := 7 // title + blank + footer + status
	limit := m.height - reserve
	if limit < 3 || limit >= len(body) {
		return body
	}
	start := 0
	if m.cursor > limit-2 {
		start = m.cursor - (limit - 2)
	}
	end := min(len(body), start+limit)
	return body[start:end]
}

func (m *Model) footerStatus() string {
	if m.status == "" {
		return ""
	}
	if m.statusErr {
		return styleError.Render("! "+m.status) + "\n"
	}
	return styleOK.Render("✓ "+m.status) + "\n"
}

func (m *Model) viewProviders() []string {
	// Column widths come from the data: provider names differ by an order of
	// magnitude ("Kiro" vs "Volcengine Ark / BytePlus ModelArk"), so a fixed
	// padding either wastes space or misaligns every column after it.
	nameW, activeW, idW := 0, 0, 0
	for _, p := range m.providers {
		nameW = max(nameW, len(p.DisplayName))
		activeW = max(activeW, len(activeOrDash(p.ActiveName)))
		idW = max(idW, len(p.Identity))
	}
	nameW = min(nameW, columnCap)
	activeW = min(activeW, columnCap)
	idW = min(idW, columnCap)

	rows := make([]string, 0, len(m.providers))
	for i, p := range m.providers {
		mark := " "
		if i == m.cursor {
			mark = "▸"
		}
		line := fmt.Sprintf("%s %-*s %-*s %-*s %d saved",
			mark,
			nameW, truncate(p.DisplayName, nameW),
			activeW, truncate(activeOrDash(p.ActiveName), activeW),
			idW, truncate(p.Identity, idW),
			p.SavedCount)
		// "no saved logins" is the relation hint for an empty provider and
		// duplicates the count column, so it is only shown when it adds
		// information.
		if hint := p.Relation.hint(); hint != "" && hint != "no saved logins" {
			line += "  " + styleWarn.Render(hint)
		}
		if p.ProxyLabel != "" {
			line += "  " + styleDim.Render(p.ProxyLabel)
		}
		if p.Error != "" {
			line += "  " + styleError.Render("status unavailable")
		}
		if i == m.cursor {
			rows = append(rows, styleSelected.Render(line))
		} else {
			rows = append(rows, line)
		}
	}
	return rows
}

func (m *Model) viewAccounts() []string {
	now := time.Now()
	nameW, idW := 0, 0
	for _, r := range m.accounts {
		nameW = max(nameW, len(r.DisplayName()))
		idW = max(idW, len(identityDisplayText(r.Identity, "—")))
	}
	nameW = min(nameW, columnCap)
	idW = min(idW, columnCap)

	rows := make([]string, 0, len(m.accounts))
	for i, r := range m.accounts {
		mark := " "
		if i == m.cursor {
			mark = "▸"
		}
		live := "·"
		if r.LiveMatch {
			live = "●"
		}
		active := ""
		if r.Active {
			active = " " + styleSelected.Render("(active)")
		}
		line := fmt.Sprintf("%s %s %-*s %-*s %s%s",
			mark, live,
			nameW, truncate(r.DisplayName(), nameW),
			idW, truncate(identityDisplayText(r.Identity, "—"), idW),
			formatRelativeTime(r.UpdatedAt, now), active)
		if r.ProxyLabel != "" {
			line += "  " + styleDim.Render(r.ProxyLabel)
		}
		if i == m.cursor {
			rows = append(rows, styleSelected.Render(line))
		} else {
			rows = append(rows, line)
		}
	}
	return rows
}

func (m *Model) viewDetail() []string {
	if m.detail >= len(m.accounts) {
		return nil
	}
	r := m.accounts[m.detail]
	rows := []string{
		kv("Provider", r.ProviderID),
		kv("Name", r.Name),
	}
	if r.Label != "" {
		rows = append(rows, kv("Label", r.Label))
	}
	rows = append(rows,
		kv("Identity", identityDisplayText(r.Identity, "—")),
		kv("Updated", r.UpdatedAt),
		kv("Live match", boolText(r.LiveMatch)),
		kv("Active", boolText(r.Active)),
		kv("Proxy", r.ProxyStatusText()),
	)
	return rows
}

func (r AccountRow) ProxyStatusText() string {
	switch {
	case r.ProxyRunning:
		return "running"
	case r.ProxyEnabled:
		return "enabled, stopped"
	default:
		return "disabled"
	}
}

func (m *Model) viewHelp() []string {
	return []string{
		"",
		"AnyPick terminal UI",
		"",
		"  ↑/↓ or k/j   move the cursor",
		"  ⏎ or l       open the row under the cursor",
		"  esc          go back",
		"  u            use (switch to) the selected account",
		"  p            start or stop that account's proxy",
		"  d            delete the selected saved account",
		"  s            save the current live login as a new account",
		"  r            reload from disk",
		"  ?            this screen",
		"  q            quit (home screen only)",
		"",
		"Proxy endpoints are shown; proxy tokens are never displayed.",
	}
}

// columnCap stops one long value from pushing the remaining columns off a
// narrow terminal. Values longer than the cap are truncated, not wrapped —
// wrapping would desynchronise the cursor row from the row count.
const columnCap = 28

func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}

func activeOrDash(name string) string {
	if name == "" {
		return "—"
	}
	return name
}

func kv(key, value string) string {
	return fmt.Sprintf("  %-12s %s", styleDim.Render(key), value)
}

func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// providerSummary renders the header counts on the home screen.
func providerSummary(rows []ProviderRow) string {
	accounts := 0
	issues := 0
	for _, r := range rows {
		accounts += r.SavedCount
		switch r.Relation {
		case RelationDrift, RelationUnsaved, RelationError:
			issues++
		}
	}
	return fmt.Sprintf("%d providers · %d saved logins · %d needing attention",
		len(rows), accounts, issues)
}
