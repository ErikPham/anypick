// Package tui is the Go terminal UI.
//
// It replaces the Ink/React tree in src/tui (ADR 0020 §5, NATIVE-M7 row 3):
// native releases ship no Node runtime, so the terminal UI is a bubbletea
// program over the same Go services the CLI commands use. It deliberately does
// not shell out to the CLI handlers — those live in package main.
//
// Invariant #2: proxy secrets never cross an output boundary. StartProxy
// returns a token; nothing here stores or renders it.
package tui

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
)

// Relation is the live-login ↔ saved-account relationship.
//
// Ported from deriveLiveRelation in src/tui/model/identity.ts so the Go and
// TypeScript trees agree while both still exist. The important property of the
// original is preserved: it never invents a match when an identity is missing.
type Relation string

const (
	RelationMatch   Relation = "match"
	RelationDrift   Relation = "drift"
	RelationUnsaved Relation = "unsaved-live"
	RelationNoLive  Relation = "no-live"
	RelationEmpty   Relation = "empty"
	RelationUnknown Relation = "unknown"
	RelationError   Relation = "error"
)

// hint is the short secondary column shown next to a provider row.
func (r Relation) hint() string {
	switch r {
	case RelationMatch, RelationUnknown:
		return ""
	case RelationDrift:
		return "changed"
	case RelationUnsaved:
		return "save this login"
	case RelationNoLive:
		return "signed out"
	case RelationEmpty:
		return "no saved logins"
	case RelationError:
		return "unavailable"
	default:
		return ""
	}
}

// identityDisplayText keeps credential fingerprints useful for matching without
// presenting them as a human identity.
func identityDisplayText(value, fallback string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return fallback
	}
	if regexp.MustCompile(`^antigravity:[0-9a-f]{12,}$`).MatchString(v) {
		return "Gemini OAuth"
	}
	return v
}

func normalizeIdentity(value string) string {
	t := strings.TrimSpace(value)
	if t == "" {
		return ""
	}
	return strings.ToLower(t)
}

// identitiesMatch returns nil when either side is absent — "unknown", not
// "different". Conflating the two is what makes a TUI claim drift it cannot see.
func identitiesMatch(a, b string) *bool {
	na, nb := normalizeIdentity(a), normalizeIdentity(b)
	if na == "" || nb == "" {
		return nil
	}
	match := na == nb
	return &match
}

// deriveLiveRelation mirrors the TypeScript function of the same name.
func deriveLiveRelation(in relationInput) Relation {
	if in.err {
		return RelationError
	}
	if !in.livePresent && in.savedCount == 0 {
		return RelationEmpty
	}
	if !in.livePresent && in.savedCount > 0 {
		return RelationNoLive
	}

	liveNorm := normalizeIdentity(in.liveIdentity)
	matchesSaved := false
	if liveNorm != "" {
		for _, id := range in.savedIdentities {
			if n := normalizeIdentity(id); n != "" && n == liveNorm {
				matchesSaved = true
				break
			}
		}
	}

	if in.activeName == "" {
		if !matchesSaved && liveNorm != "" {
			return RelationUnsaved
		}
		return RelationUnknown
	}

	if match := identitiesMatch(in.liveIdentity, in.activeIdentity); match != nil {
		if *match {
			return RelationMatch
		}
		if !matchesSaved && liveNorm != "" {
			return RelationUnsaved
		}
		return RelationDrift
	}
	return RelationUnknown
}

type relationInput struct {
	savedCount      int
	livePresent     bool
	liveIdentity    string
	activeName      string
	activeIdentity  string
	savedIdentities []string
	err             bool
}

// ProviderRow is one line of the home screen.
type ProviderRow struct {
	ID          string
	DisplayName string
	ActiveName  string
	Identity    string
	SavedCount  int
	Relation    Relation
	ProxyLabel  string
	CanProxy    bool
	Error       string
}

// AccountRow is one line of a provider's account list.
type AccountRow struct {
	ProviderID   string
	Name         string
	Label        string
	Identity     string
	Active       bool
	LiveMatch    bool
	UpdatedAt    string
	ProxyEnabled bool
	ProxyRunning bool
	ProxyLabel   string
}

// DisplayName prefers a human label, then the account name, then the identity.
// Path-like labels ("Live ~/.codex/auth.json") are an import artifact and are
// never shown — same rule as accountDisplayName in the TypeScript tree.
func (r AccountRow) DisplayName() string {
	if l := strings.TrimSpace(r.Label); l != "" && !isPathLikeLabel(l) && !strings.EqualFold(l, "live") {
		return l
	}
	if r.Name != "" && !strings.EqualFold(r.Name, "live") {
		return r.Name
	}
	if r.Identity != "" {
		return r.Identity
	}
	if r.Name != "" {
		return r.Name
	}
	return "main"
}

func isPathLikeLabel(label string) bool {
	return strings.ContainsAny(label, `~\/`) ||
		strings.Contains(strings.ToLower(label), ".json")
}

// LoadProviders reads the home screen. A provider whose status cannot be read
// degrades to a single error row instead of aborting the whole screen — the
// TypeScript loadRootModel does the same via safeCurrent/safeList.
func LoadProviders(ctx context.Context, a *app.App) []ProviderRow {
	ids := append([]string(nil), a.Providers.IDs()...)
	sort.Strings(ids)

	rows := make([]ProviderRow, 0, len(ids))
	for _, id := range ids {
		pv, err := a.Providers.Get(id)
		if err != nil {
			rows = append(rows, ProviderRow{ID: id, DisplayName: id, Relation: RelationError, Error: err.Error()})
			continue
		}
		row := ProviderRow{ID: id, DisplayName: pv.Name(), CanProxy: pv.CanProxy()}

		cur, err := a.Accounts.Current(ctx, id)
		if err != nil {
			row.Relation = RelationError
			row.Error = err.Error()
			row.Identity = "—"
			rows = append(rows, row)
			continue
		}

		accounts, listErr := a.Store.ListAccounts(ctx, id)
		if listErr != nil {
			accounts = nil
		}
		row.SavedCount = len(accounts)
		row.ActiveName = cur.Active

		var activeIdentity string
		if cur.Account != nil {
			activeIdentity = cur.Account.Meta.Identity
		}
		savedIdentities := make([]string, 0, len(accounts))
		for _, acct := range accounts {
			savedIdentities = append(savedIdentities, acct.Meta.Identity)
		}

		row.Relation = deriveLiveRelation(relationInput{
			savedCount:      len(accounts),
			livePresent:     cur.Live.Present,
			liveIdentity:    cur.Live.Identity,
			activeName:      cur.Active,
			activeIdentity:  activeIdentity,
			savedIdentities: savedIdentities,
		})

		switch row.Relation {
		case RelationError:
			row.Identity = "—"
		case RelationEmpty, RelationNoLive:
			row.Identity = "signed out"
		default:
			row.Identity = identityDisplayText(cur.Live.Identity, "signed out")
		}

		if row.CanProxy && row.ActiveName != "" {
			if st, stErr := a.Proxy.StatusContext(ctx, id, row.ActiveName); stErr == nil && st != nil {
				row.ProxyLabel = formatProxyLabel(st)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// LoadAccounts reads one provider's saved accounts.
func LoadAccounts(ctx context.Context, a *app.App, providerID string) ([]AccountRow, error) {
	accounts, err := a.Store.ListAccounts(ctx, providerID)
	if err != nil {
		return nil, err
	}
	active, _ := a.Store.GetActive(ctx, providerID)

	cur, curErr := a.Accounts.Current(ctx, providerID)
	liveIdentity := ""
	if curErr == nil && cur != nil {
		liveIdentity = cur.Live.Identity
	}

	rows := make([]AccountRow, 0, len(accounts))
	for _, acct := range accounts {
		row := AccountRow{
			ProviderID: providerID,
			Name:       acct.Meta.Name,
			Label:      acct.Meta.Label,
			Identity:   acct.Meta.Identity,
			Active:     acct.Meta.Name == active,
			UpdatedAt:  acct.Meta.UpdatedAt,
		}
		if match := identitiesMatch(liveIdentity, acct.Meta.Identity); match != nil && *match {
			row.LiveMatch = true
		}
		if st, stErr := a.Proxy.StatusContext(ctx, providerID, acct.Meta.Name); stErr == nil && st != nil {
			row.ProxyEnabled = st.Enabled
			row.ProxyRunning = st.Running
			row.ProxyLabel = formatProxyLabel(st)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Active != rows[j].Active {
			return rows[i].Active
		}
		return rows[i].DisplayName() < rows[j].DisplayName()
	})
	return rows, nil
}

// formatProxyLabel mirrors formatProxyPortLabel: show a port only when the
// proxy is actually serving, and let a real unavailable reason through
// verbatim rather than hiding it behind a neutral word.
func formatProxyLabel(st *proxy.Status) string {
	if st == nil {
		return ""
	}
	if st.Running && st.Port != 0 {
		return fmt.Sprintf("proxy :%d", st.Port)
	}
	if st.Running {
		if hostport, err := endpointHostPort(st.Endpoint); err == nil {
			return "proxy " + hostport
		}
		return "proxy running"
	}
	if st.Enabled && !st.Running {
		detail := strings.ToLower(st.Detail)
		if strings.Contains(detail, "kirolink") || strings.Contains(detail, "missing") {
			return st.Detail
		}
		return "proxy stopped"
	}
	return ""
}

// endpointHostPort strips a scheme from a stored endpoint. The TUI shows
// host:port only — a proxy URL is not secret, but it is also not useful here,
// and printing it invites copy-pasting a URL that embeds nothing but noise.
func endpointHostPort(endpoint string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Port() == "" {
		return "", fmt.Errorf("endpoint has no port")
	}
	return u.Host, nil
}

// formatRelativeTime mirrors formatRelativeTime in the TypeScript tree.
func formatRelativeTime(iso string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	diff := now.Sub(t)
	if diff < 0 {
		diff = 0
	}
	sec := int(diff.Seconds())
	switch {
	case sec < 45:
		return "just now"
	case sec < 60*60:
		return fmt.Sprintf("%dm ago", sec/60)
	case sec < 48*60*60:
		return fmt.Sprintf("%dh ago", sec/(60*60))
	case sec < 60*24*60*60:
		return fmt.Sprintf("%dd ago", sec/(24*60*60))
	case sec < 24*30*24*60*60:
		return fmt.Sprintf("%dmo ago", sec/(30*24*60*60))
	default:
		return fmt.Sprintf("%dy ago", sec/(365*24*60*60))
	}
}

// suggestAccountName derives a save name from a live identity, preferring the
// email local-part. Mirrors suggestAccountSlug in the TypeScript tree.
//
// The name is a suggestion only — Save still runs it through slug.AccountName,
// and a collision returns an error rather than silently overwriting the
// existing account (force is always false here).
func suggestAccountName(identity string) string {
	raw := strings.TrimSpace(identity)
	if raw == "" {
		return "main"
	}
	local := raw
	if at := strings.Index(raw, "@"); at > 0 {
		local = raw[:at]
	}
	if name, err := slug.AccountName(local); err == nil && name != "" {
		return name
	}
	return "main"
}

// ── Apps board (the product) ────────────────────────────────────
//
// The Apps board answers "what should Claude / Codex / ZCode use right now?"
// for each routing app. A client appears here only if it is listed in
// routingAppIDs; a client that is not listed must instead have a provider of
// its own that AnyPick can log in for (gemini, kiro, opencode, workbuddy) —
// that is what makes it "login-only" and switchable under Accounts.
// TestEveryRegisteredClientIsReachableInTheTUI fails the build when a client
// satisfies neither: an unlisted client with no provider of its own is simply
// invisible in the TUI, which is how zcode shipped for a while.
//
// The list is explicit rather than a "client that is not also a provider"
// heuristic because claude and codex *are* providers (AnyPick manages their
// native logins) and still have to route.

type AppStatus string

const (
	AppReady     AppStatus = "ready"
	AppNative    AppStatus = "native"
	AppNotSet    AppStatus = "not set"
	AppAttention AppStatus = "attention"
	AppFailed    AppStatus = "failed"
)

// AppRow is one line of the Apps board.
type AppRow struct {
	ClientID    string
	DisplayName string
	SourceText  string
	ModelText   string
	Status      AppStatus
}

// nativeProviderByClient maps a routing app to its own credential provider.
// Only claude/codex route, and claude's native provider is anthropic while
// codex's is openai. There is no protocol method on the provider interface to
// derive this, so the known routing apps are listed explicitly; the fallback
// returns "" (unknown native provider) rather than guessing.
var nativeProviderByClient = map[string]string{
	"claude": "anthropic",
	"codex":  "openai",
}

func clientNativeProvider(a *app.App, clientID string) string {
	if p, ok := nativeProviderByClient[clientID]; ok && a.Providers.Has(p) {
		return p
	}
	return ""
}

// routingAppAllowlist is the explicit set of clients the Apps board routes.
// Login-only clients (gemini, kiro, opencode, workbuddy) are excluded by
// design — they switch their own provider login under Accounts. zcode and dsh
// are the opposite case: neither has a provider of its own, so this board is
// the only surface that can reach them at all. dsh is a Cordis-based harness
// whose OpenAI-compatible endpoint is pointed at the AnyPick managed proxy;
// zcode's `zai` entry in the compatible list is a plain API-key provider that
// never touches ~/.zcode. Both are deliberately absent from
// nativeProviderByClient — AnyPick does not manage a ZCode or DeepSeek Harness
// login, so claiming one would make the row lie about being "native".
var routingAppAllowlist = []string{"claude", "codex", "zcode", "dsh"}

// routingAppIDs returns the client ids that are routing apps, filtered against
// the live client registry so an app absent from a particular build simply does
// not appear.
func routingAppIDs(a *app.App) []string {
	out := make([]string, 0, len(routingAppAllowlist))
	for _, id := range routingAppAllowlist {
		if a.Clients.Has(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func providerShortName(a *app.App, providerID string) string {
	if p, err := a.Providers.Get(providerID); err == nil {
		return provider.ShortNameOf(p)
	}
	return providerID
}

// sourceExists reports whether a binding's source still resolves to a real
// object, so a route whose backing account/gateway/preset was deleted shows
// "attention" instead of a stale "ready".
func sourceExists(ctx context.Context, a *app.App, ref refs.Ref) bool {
	switch ref.Kind {
	case refs.KindGateway:
		names, err := a.Runtime.Profiles.ListContext(ctx)
		if err != nil {
			return false
		}
		for _, n := range names {
			if n == ref.Name {
				return true
			}
		}
		return false
	case refs.KindAccount, refs.KindAccountPool:
		accounts, err := a.Store.ListAccounts(ctx, ref.Provider)
		if err != nil {
			return false
		}
		for _, acct := range accounts {
			if acct.Meta.Name == ref.Name {
				return true
			}
		}
		return false
	case refs.KindPreset:
		saved, err := a.Presets.ListContext(ctx)
		if err != nil {
			return false
		}
		for _, p := range saved {
			if p.Name == ref.Name {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// LoadApps reads the Apps board.
func LoadApps(ctx context.Context, a *app.App) []AppRow {
	ids := routingAppIDs(a)
	rows := make([]AppRow, 0, len(ids))
	for _, id := range ids {
		row := AppRow{ClientID: id}
		if cad, err := a.Clients.Get(id); err == nil {
			row.DisplayName = cad.Name()
		} else {
			row.DisplayName = id
		}
		native := clientNativeProvider(a, id)
		binding, _ := a.Bindings.GetGlobalContext(ctx, id)
		if binding != nil && binding.Spec.Source.Kind != "" {
			row.SourceText = refs.DisplayRef(binding.Spec.Source)
			row.ModelText = binding.Spec.Model.ID
			if row.ModelText == "" {
				row.ModelText = "(model set by client)"
			}
			if sourceExists(ctx, a, binding.Spec.Source) {
				row.Status = AppReady
			} else {
				row.Status = AppAttention
			}
		} else if native != "" {
			if cur, err := a.Accounts.Current(ctx, native); err == nil && cur.Live.Present {
				row.Status = AppNative
				row.SourceText = "own login"
				row.ModelText = "native"
			} else {
				row.Status = AppNotSet
				row.SourceText = "not connected"
				row.ModelText = "—"
			}
		} else {
			row.Status = AppNotSet
			row.SourceText = "not connected"
			row.ModelText = "—"
		}
		rows = append(rows, row)
	}
	return rows
}

// ── Route-build wizard: source options ──────────────────────────

// SourceOption is one selectable source in the wizard's first step.
type SourceOption struct {
	Ref   refs.Ref
	Group string // "Native accounts" | "Gateways & proxies" | "Saved setups"
	Label string // primary line
	Sub   string // secondary line
}

// LoadSourceOptions lists every safe pairing the wizard may route an app
// through. Only compatible sources appear — an empty list means "save an
// account (a) or add a gateway (g) first", matching the Node spec.
func LoadSourceOptions(ctx context.Context, a *app.App, clientID string) []SourceOption {
	native := clientNativeProvider(a, clientID)
	var opts []SourceOption

	if native != "" {
		if accounts, err := a.Store.ListAccounts(ctx, native); err == nil {
			for _, acct := range accounts {
				label := fmt.Sprintf("%s · %s", providerShortName(a, native), identityDisplayText(acct.Meta.Identity, acct.Meta.Name))
				sub := "saved login"
				if cur, cerr := a.Accounts.Current(ctx, native); cerr == nil && cur.Active == acct.Meta.Name {
					sub = "current live login"
				}
				opts = append(opts, SourceOption{
					Ref:   refs.AccountRef(native, acct.Meta.Name),
					Group: "Native accounts",
					Label: label,
					Sub:   sub,
				})
			}
		}
	}

	if names, err := a.Runtime.Profiles.ListContext(ctx); err == nil {
		for _, name := range names {
			sub := "gateway"
			if meta, merr := a.Runtime.Profiles.GetContext(ctx, name); merr == nil && meta.Meta.DefaultModel != "" {
				sub = "gateway · default model set"
			}
			opts = append(opts, SourceOption{
				Ref:   refs.GatewayRef(name),
				Group: "Gateways & proxies",
				Label: name,
				Sub:   sub,
			})
		}
	}

	for _, pid := range a.Providers.IDs() {
		p, perr := a.Providers.Get(pid)
		if perr != nil || !p.CanProxy() {
			continue
		}
		accounts, aerr := a.Store.ListAccounts(ctx, pid)
		if aerr != nil {
			continue
		}
		for _, acct := range accounts {
			opts = append(opts, SourceOption{
				Ref:   refs.AccountRef(pid, acct.Meta.Name),
				Group: "Gateways & proxies",
				Label: fmt.Sprintf("%s/%s", pid, acct.Meta.Name),
				Sub:   "account proxy",
			})
		}
	}

	if saved, err := a.Presets.ListContext(ctx); err == nil {
		for _, p := range saved {
			if p.Spec.Client != clientID {
				continue
			}
			opts = append(opts, SourceOption{
				Ref:   refs.PresetRef(p.Name),
				Group: "Saved setups",
				Label: "@" + p.Name,
				Sub:   "preset",
			})
		}
	}

	return opts
}

// ── Gateways board ──────────────────────────────────────────────

// GatewayRow is one line of the Gateways board. The endpoint URL is shown
// (not a secret); the API key is never read here.
type GatewayRow struct {
	Name         string
	Provider     string
	DefaultModel string
	Endpoint     string
}

// LoadGateways reads stored gateway profiles.
func LoadGateways(ctx context.Context, a *app.App) []GatewayRow {
	names, err := a.Runtime.Profiles.ListContext(ctx)
	if err != nil {
		return nil
	}
	rows := make([]GatewayRow, 0, len(names))
	for _, name := range names {
		row := GatewayRow{Name: name}
		if meta, merr := a.Runtime.Profiles.GetContext(ctx, name); merr == nil && meta != nil {
			row.Provider = meta.Meta.Provider
			row.DefaultModel = meta.Meta.DefaultModel
			row.Endpoint = meta.Meta.Endpoint
		}
		rows = append(rows, row)
	}
	return rows
}

// ── Proxy board ─────────────────────────────────────────────────
//
// Read-only for now (full start/stop/pool editing lands in Phase 3). The board
// lists every account-proxy source with its running status so the user can see
// what is serving.

// ProxyRow is one line of the Proxy board.
type ProxyRow struct {
	Provider string
	Account  string
	RefText  string
	Running  bool
	Endpoint string
	Enabled  bool
}

// LoadProxies reads account-proxy status for every CanProxy provider.
func LoadProxies(ctx context.Context, a *app.App) []ProxyRow {
	var rows []ProxyRow
	for _, pid := range a.Providers.IDs() {
		p, perr := a.Providers.Get(pid)
		if perr != nil || !p.CanProxy() {
			continue
		}
		accounts, aerr := a.Store.ListAccounts(ctx, pid)
		if aerr != nil {
			continue
		}
		for _, acct := range accounts {
			row := ProxyRow{
				Provider: pid,
				Account:  acct.Meta.Name,
				RefText:  fmt.Sprintf("%s/%s", pid, acct.Meta.Name),
			}
			if st, serr := a.Proxy.StatusContext(ctx, pid, acct.Meta.Name); serr == nil && st != nil {
				row.Enabled = st.Enabled
				row.Running = st.Running
				row.Endpoint = st.Endpoint
			}
			rows = append(rows, row)
		}
	}
	return rows
}
