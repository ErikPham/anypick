package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/doctor"
)

// Styles. Adaptive colors keep the TUI readable on both light and dark
// terminals instead of assuming the developer's theme.
var (
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

// View renders the whole screen. The wizard and help are full-screen takeovers
// so the user never sees a half-rendered board behind an overlay.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.wizard != nil {
		return m.viewWizard()
	}
	if m.showHelp {
		return m.viewHelpFull()
	}
	if m.diagnosis != nil {
		return m.viewDiagnosis()
	}

	width := m.width
	if width == 0 {
		width = 80
	}

	var body []string
	var title string
	var hints string
	switch m.board {
	case boardApps:
		title = "anypick / apps"
		body = m.viewApps()
		hints = "↑↓ move · ⏎ connect/change · a accounts · g gateways · p proxy · d diagnose · ? help · q quit"
	case boardAccounts:
		title = "anypick / accounts"
		if m.accountDrill {
			body = m.viewAccounts()
			hints = "↑↓ move · ⏎ use · v details · p proxy · s/a save live · d delete · r reload · esc back"
		} else {
			body = m.viewProviders()
			if m.filtering {
				hints = "filter: " + m.filterText + "_ · ⏎ apply · esc clear"
			} else {
				hints = "↑↓ move · ⏎ open · a add login · v details · i/e import-export · f filter · esc apps · ? help"
			}
		}
	case boardGateways:
		title = "anypick / gateways"
		body = m.viewGateways()
		hints = "↑↓ move · esc apps · r reload · ? help"
	case boardProxy:
		title = "anypick / proxy"
		body = m.viewProxies()
		hints = "↑↓ move · esc apps · r reload · ? help"
	}

	var out strings.Builder
	out.WriteString(styleHeader.Render(title))
	out.WriteString("\n\n")

	if len(body) == 0 {
		out.WriteString(styleDim.Render("  (nothing to show)") + "\n")
	} else {
		visible := m.visibleRows(body)
		out.WriteString(strings.Join(visible, "\n") + "\n")
	}

	if m.board == boardApps && len(m.apps) > 0 {
		out.WriteString(styleDim.Render(dashed(width)) + "\n")
		for _, h := range m.appsHint() {
			out.WriteString(h + "\n")
		}
	}

	out.WriteString("\n" + styleFooter.Render(hints) + "\n")
	out.WriteString(m.footerStatus())
	if m.confirm != nil {
		out.WriteString("\n" + styleWarn.Render(m.confirm.question+" (y/n)") + "\n")
	}
	if m.prompting {
		out.WriteString("\n" + styleSelected.Render(m.promptLabel) + "\n")
		out.WriteString(styleOK.Render(m.promptText+"_") + "\n")
	}
	return out.String()
}

// noColor reports whether NO_COLOR is set (plain terminals fall back to
// ASCII glyphs, per docs/guides/terminal-ui.mdx).
func (m *Model) noColor() bool { return os.Getenv("NO_COLOR") != "" }

func glyph(colored, plain string, noColor bool) string {
	if noColor {
		return plain
	}
	return colored
}

func appStatusGlyph(s AppStatus, noColor bool) string {
	switch s {
	case AppReady:
		return glyph("●", "*", noColor)
	case AppNotSet, AppNative:
		return glyph("○", "o", noColor)
	case AppAttention:
		return glyph("◐", "!", noColor)
	case AppFailed:
		return glyph("×", "x", noColor)
	}
	return "?"
}

func dashed(width int) string {
	if width < 4 {
		width = 4
	}
	return strings.Repeat("─", width-4)
}

func clientName(a *app.App, clientID string) string {
	if cad, err := a.Clients.Get(clientID); err == nil {
		return cad.Name()
	}
	return clientID
}

func (m *Model) viewApps() []string {
	noColor := m.noColor()
	nameW := 0
	for _, r := range m.apps {
		nameW = max(nameW, len(r.DisplayName))
	}
	nameW = min(nameW, 16)

	rows := make([]string, 0, len(m.apps))
	for i, r := range m.apps {
		mark := " "
		if i == m.cursor {
			mark = "▸"
		}
		mid := r.SourceText
		if r.ModelText != "" && r.ModelText != "—" {
			mid += " · " + r.ModelText
		}
		line := fmt.Sprintf("%s %-*s %-*s %s %s",
			mark,
			nameW, truncate(r.DisplayName, nameW),
			32, truncate(mid, 32),
			appStatusGlyph(r.Status, noColor),
			string(r.Status))
		if i == m.cursor {
			rows = append(rows, styleSelected.Render(line))
		} else {
			rows = append(rows, line)
		}
	}
	return rows
}

func (m *Model) appsHint() []string {
	if len(m.apps) == 0 || m.cursor >= len(m.apps) {
		return nil
	}
	r := m.apps[m.cursor]
	switch r.Status {
	case AppReady:
		return []string{
			fmt.Sprintf("%s uses %s", r.DisplayName, r.SourceText),
			fmt.Sprintf("model %s · enter to change route", r.ModelText),
		}
	case AppNative:
		return []string{
			fmt.Sprintf("%s uses its own login", r.DisplayName),
			"enter to let AnyPick take over",
		}
	default:
		return []string{
			fmt.Sprintf("%s is %s", r.DisplayName, string(r.Status)),
			"enter to connect app",
		}
	}
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

func (m *Model) viewGateways() []string {
	rows := make([]string, 0, len(m.gateways))
	for i, r := range m.gateways {
		mark := " "
		if i == m.cursor {
			mark = "▸"
		}
		line := fmt.Sprintf("%s %s", mark, r.Name)
		var extra []string
		if r.Provider != "" {
			extra = append(extra, r.Provider)
		}
		if r.DefaultModel != "" {
			extra = append(extra, "model "+r.DefaultModel)
		}
		if r.Endpoint != "" {
			extra = append(extra, r.Endpoint)
		}
		if len(extra) > 0 {
			line += "   " + styleDim.Render(strings.Join(extra, " · "))
		}
		if i == m.cursor {
			rows = append(rows, styleSelected.Render(line))
		} else {
			rows = append(rows, line)
		}
	}
	return rows
}

func (m *Model) viewProxies() []string {
	rows := make([]string, 0, len(m.proxies))
	for i, r := range m.proxies {
		mark := " "
		if i == m.cursor {
			mark = "▸"
		}
		status := "stopped"
		if r.Running {
			status = "running " + r.Endpoint
		} else if r.Enabled {
			status = "enabled, stopped"
		}
		extra := []string{status}
		line := fmt.Sprintf("%s %-20s %s", mark, r.RefText, styleDim.Render(strings.Join(extra, " · ")))
		if i == m.cursor {
			rows = append(rows, styleSelected.Render(line))
		} else {
			rows = append(rows, line)
		}
	}
	return rows
}

func (m *Model) viewWizard() string {
	w := m.wizard
	stepName := map[wizardStep]string{
		wizardSource:  "source",
		wizardModels:  "models",
		wizardPreview: "preview",
	}[w.step]
	title := fmt.Sprintf("anypick / apps / %s / %s", clientName(m.app, w.clientID), stepName)

	var body []string
	switch w.step {
	case wizardSource:
		body = m.viewWizardSource()
	case wizardModels:
		body = m.viewWizardModels()
	case wizardPreview:
		body = m.viewWizardPreview()
	}

	var footer string
	switch w.step {
	case wizardSource:
		footer = "↑↓ move · ⏎ choose route · esc back"
	case wizardModels:
		footer = "↑↓ move · ⏎ edit · a preview · esc back"
	case wizardPreview:
		if w.planErr != "" {
			footer = "esc back"
		} else {
			footer = "⏎ activate route · esc back"
		}
	}

	var out strings.Builder
	out.WriteString(styleHeader.Render(title))
	out.WriteString("\n\n")
	if len(body) == 0 {
		out.WriteString(styleDim.Render("  (no options — save an account with a, or add a gateway with g)") + "\n")
	} else {
		out.WriteString(strings.Join(body, "\n") + "\n")
	}
	out.WriteString("\n" + styleFooter.Render(footer) + "\n")
	out.WriteString(m.footerStatus())
	return out.String()
}

func (m *Model) viewWizardSource() []string {
	w := m.wizard
	var lines []string
	lastGroup := ""
	for i, o := range w.sources {
		if o.Group != lastGroup {
			lines = append(lines, styleDim.Render(o.Group))
			lastGroup = o.Group
		}
		mark := " "
		if i == w.cursor {
			mark = "▸"
		}
		line := fmt.Sprintf("%s %s", mark, o.Label)
		if o.Sub != "" {
			line += "   " + styleDim.Render(o.Sub)
		}
		if i == w.cursor {
			lines = append(lines, styleSelected.Render(line))
		} else {
			lines = append(lines, line)
		}
	}
	return lines
}

func (m *Model) viewWizardModels() []string {
	w := m.wizard
	var lines []string

	// ModelsForClient discovery: surface the real model list for the chosen
	// source so the user can type a valid id instead of guessing.
	if prov := w.sourceProvider; prov != "" {
		if pol := catalog.Policy(prov); pol != nil {
			seen := make(map[string]bool, len(pol.RoleFriendly)+len(pol.StaticFallback))
			var suggested []string
			for _, id := range append(append([]string{}, pol.RoleFriendly...), pol.StaticFallback...) {
				if !seen[id] {
					seen[id] = true
					suggested = append(suggested, id)
				}
			}
			if len(suggested) > 0 {
				line := "available models: " + strings.Join(suggested, ", ")
				if len(line) > 200 {
					line = line[:197] + "…"
				}
				lines = append(lines, styleDim.Render(line))
			}
		}
	}

	for i, f := range w.fields {
		mark := " "
		if i == w.cursor {
			mark = "▸"
		}
		val := w.model
		if f != "default" {
			val = w.roles[f]
		}
		if val == "" && f != "default" {
			val = "(same as default)"
		}
		if i == w.editingField {
			val = "[" + w.text + "_]"
		}
		line := fmt.Sprintf("%s %-8s %s", mark, f, val)
		if i == w.cursor {
			lines = append(lines, styleSelected.Render(line))
		} else {
			lines = append(lines, line)
		}
	}
	return lines
}

func (m *Model) viewWizardPreview() []string {
	w := m.wizard
	if w.planErr != "" {
		lines := []string{styleError.Render("Could not plan this route:")}
		for _, line := range strings.Split(w.planErr, "\n") {
			lines = append(lines, styleError.Render("  "+line))
		}
		lines = append(lines, "", styleDim.Render("esc to go back and pick a different source"))
		return lines
	}
	if w.plan == nil {
		return []string{styleDim.Render("planning…")}
	}
	lines := []string{
		fmt.Sprintf("Activate this route for %s?", clientName(m.app, w.clientID)),
		"",
	}
	for _, s := range w.plan.Steps {
		lines = append(lines, " · "+planStepLabel(s.Kind))
	}
	return lines
}

func planStepLabel(k activation.StepKind) string {
	switch k {
	case activation.StepResolveSource:
		return "resolve source"
	case activation.StepExpandPreset:
		return "expand preset"
	case activation.StepValidateCompatibility:
		return "validate compatibility"
	case activation.StepValidateCredential:
		return "validate credential"
	case activation.StepInspectClientState:
		return "inspect client state"
	case activation.StepResolveTransport:
		return "resolve transport"
	case activation.StepValidateExternalDep:
		return "validate external dependency"
	case activation.StepAllocateProxyLease:
		return "allocate proxy lease"
	case activation.StepStartProxy:
		return "start local proxy if needed"
	case activation.StepWaitForHealth:
		return "wait for proxy health"
	case activation.StepEnsureProxyHub:
		return "ensure proxy hub"
	case activation.StepAttachProxyHubRoute:
		return "attach proxy hub route"
	case activation.StepWaitForHubHealth:
		return "wait for hub health"
	case activation.StepValidateProxyHubRoute:
		return "validate proxy hub route"
	case activation.StepWriteNativeAuth:
		return "write native auth"
	case activation.StepWriteClientConfig:
		return "write client config"
	case activation.StepCreateEnvironmentOverlay:
		return "create environment overlay"
	case activation.StepCreateTempClientHome:
		return "create temporary client home"
	case activation.StepVerifyEffectiveState:
		return "verify effective state"
	case activation.StepCommitGlobalBinding:
		return "commit global binding"
	case activation.StepCommitProjectBinding:
		return "commit project binding"
	case activation.StepSpawnChild:
		return "spawn managed client"
	case activation.StepReleaseLease:
		return "release proxy lease"
	case activation.StepRestoreTemporaryState:
		return "restore temporary state"
	}
	return strings.ToLower(string(k))
}

// viewDiagnosis is the read-only health report overlay for one routing client
// (the Apps board "d diagnose"). It dismisses on any key, so it is a full
// takeover like help — the underlying board is not half-rendered behind it.
func (m *Model) viewDiagnosis() string {
	rep := m.diagnosis
	if rep == nil {
		return ""
	}

	// Every branch assigns style, so it needs no zero value here — giving it
	// one is dead code staticcheck flags.
	label := ""
	var style lipgloss.Style
	switch rep.Status {
	case doctor.StatusOK:
		label, style = "healthy", styleOK
	case doctor.StatusDegraded:
		label, style = "needs attention", styleWarn
	case doctor.StatusUnhealthy:
		label, style = "unhealthy", styleError
	default:
		label, style = "not connected", styleDim
	}

	var out strings.Builder
	out.WriteString(styleHeader.Render("anypick / diagnose / "+clientName(m.app, rep.ClientID)) + "  ")
	out.WriteString(style.Render(label) + "\n\n")

	if len(rep.Findings) == 0 {
		out.WriteString(styleDim.Render("  (no checks ran)") + "\n")
	}
	for _, f := range rep.Findings {
		var g string
		switch f.Status {
		case "ok":
			g = styleOK.Render("✓")
		case "warn":
			g = styleWarn.Render("!")
		case "fail":
			g = styleError.Render("×")
		default:
			g = styleDim.Render("·")
		}
		line := fmt.Sprintf("  %s %s", g, f.Check)
		if f.Detail != "" {
			line += " — " + f.Detail
		}
		out.WriteString(line + "\n")
	}

	out.WriteString("\n" + styleFooter.Render("any key to dismiss") + "\n")
	return out.String()
}

func (m *Model) viewHelpFull() string {
	lines := []string{
		"",
		"AnyPick terminal UI",
		"",
		"  Apps       what Claude / Codex use right now (the product)",
		"  Accounts   save, switch, refresh, delete logins",
		"  Gateways   endpoint + key profiles bound to apps",
		"  Proxy      local proxies, pools, hub",
		"",
		"  ↑/↓ or k/j   move cursor      tab   cycle boards      esc   back",
		"  ⏎           connect / change route (Apps) or use account (Accounts)",
		"  a g p       jump to Accounts / Gateways / Proxy from Apps",
		"  d           diagnose               ?   this screen      q   quit (Apps)",
		"",
		"Proxy endpoints and hub keys are shown; account proxy tokens are never displayed.",
	}
	var out strings.Builder
	out.WriteString(styleHeader.Render("anypick / help") + "\n\n")
	out.WriteString(strings.Join(lines, "\n") + "\n")
	return out.String()
}

// columnCap stops one long value from pushing the remaining columns off a
// narrow terminal. Values longer than the cap are truncated, not wrapped.
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
