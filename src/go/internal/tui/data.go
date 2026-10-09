// Package tui is the Go terminal UI.
//
// It replaces the prior terminal UI (ADR 0020 §5, NATIVE-M7 row 3):
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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/desktop"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/slug"
)

// Relation is the live-login ↔ saved-account relationship.
//
// It never invents a match when an identity is missing.
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
	if safe := desktop.DisplaySafeIdentity(v); safe != "" {
		return safe
	}
	return fallback
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
// never shown.
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
// degrades to a single error row instead of aborting the whole screen.
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

// formatProxyLabel shows a port only when the proxy is actually serving, and
// lets a real unavailable reason through verbatim rather than hiding it
// behind a neutral word.
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
// email local-part.
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

// clientNativeProvider reports which registered provider serves clientID as its
// own login, or "" when no provider claims it.
//
// The answer is not the identity: zai answers for zcode. It is also not a
// hand-kept table of client→provider pairs, which is how catalog ids
// ("anthropic", "openai") came to be checked against a registry keyed by
// provider ids and silently resolved nothing. NativeClientProvider is the
// capability that knows, so the answer is derived from the registry — the
// inverse of the desktop snapshot's nativeClientIDOf, under the same rule:
// a provider that borrows another client's login publishes it, and every other
// provider owns its login under its own id.
// appendProxySourceOptions emits the Gateways & proxies account rows: every
// CanProxy provider's accounts, minus the refs the native block already
// emitted — the same account under two groups with two subs was reachable for
// zcode (native provider zai, which CanProxy), and the desktop refuses this
// shape explicitly.
func appendProxySourceOptions(ctx context.Context, a *app.App, clientID string, opts []SourceOption) []SourceOption {
	seenAccountRefs := map[string]bool{}
	for _, opt := range opts {
		if r := opt.Ref; r.Kind == refs.KindAccount {
			seenAccountRefs[r.Provider+"/"+r.Name] = true
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
			key := pid + "/" + acct.Meta.Name
			if seenAccountRefs[key] {
				continue
			}
			seenAccountRefs[key] = true
			opts = append(opts, SourceOption{
				Ref:   refs.AccountRef(pid, acct.Meta.Name),
				Group: "Gateways & proxies",
				Label: fmt.Sprintf("%s/%s", pid, acct.Meta.Name),
				Sub:   "account proxy",
			})
		}
	}
	return opts
}

func clientNativeProvider(a *app.App, clientID string) string {
	if a == nil || a.Providers == nil || clientID == "" {
		return ""
	}
	for _, providerID := range a.Providers.IDs() {
		pv, err := a.Providers.Get(providerID)
		if err != nil {
			continue
		}
		native := providerID
		if ncp, ok := pv.(provider.NativeClientProvider); ok {
			if id := strings.TrimSpace(ncp.NativeClientID()); id != "" {
				native = id
			}
		}
		if native == clientID {
			return providerID
		}
	}
	return ""
}

// routingAppAllowlist is the explicit set of clients the Apps board routes.
// dsh, commandcode, pi and omp have no provider that claims them as its native
// client, so this board is the only surface that reaches them. dsh is a
// Cordis-based harness whose OpenAI-compatible endpoint is pointed at the
// AnyPick managed proxy; the others follow their configured AnyPick entries.
// zcode belongs here as the native client of the zai provider, exactly as the
// desktop snapshot publishes it: the Z.ai subscription login is both the
// account managed under Accounts and this app's own login. opencode routes
// here too: its adapter accepts a gateway source and the CLI already binds
// one, so a TUI-only user could not point OpenCode at anything.
// The list is explicit rather than a "client that is not also a provider"
// heuristic because claude and codex *are* providers (AnyPick manages their
// native logins) and still have to route.
var routingAppAllowlist = []string{"claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp"}

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
		names, err := a.Runtime.ProfileSvc.ListContext(ctx)
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
		saved, err := a.PresetSvc.ListContext(ctx)
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
		binding, _ := a.BindingSvc.GetGlobalContext(ctx, id)
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

// SourceOption is one selectable source in the wizard's first step.
type SourceOption struct {
	Ref   refs.Ref
	Group string // "Native accounts" | "Gateways & proxies" | "Saved setups"
	Label string // primary line
	Sub   string // secondary line
}

// LoadSourceOptions lists every safe pairing the wizard may route an app
// through. Only compatible sources appear — an empty list means "save an
// account (a) or add a gateway (g) first", as the terminal-ui guide describes.
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

	if names, err := a.Runtime.ProfileSvc.ListContext(ctx); err == nil {
		for _, name := range names {
			sub := "gateway"
			if meta, merr := a.Runtime.ProfileSvc.GetContext(ctx, name); merr == nil && meta.Meta.DefaultModel != "" {
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

	opts = appendProxySourceOptions(ctx, a, clientID, opts)

	if saved, err := a.PresetSvc.ListContext(ctx); err == nil {
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
	names, err := a.Runtime.ProfileSvc.ListContext(ctx)
	if err != nil {
		return nil
	}
	rows := make([]GatewayRow, 0, len(names))
	for _, name := range names {
		row := GatewayRow{Name: name}
		if meta, merr := a.Runtime.ProfileSvc.GetContext(ctx, name); merr == nil && meta != nil {
			row.Provider = meta.Meta.Provider
			row.DefaultModel = meta.Meta.DefaultModel
			row.Endpoint = meta.Meta.Endpoint
		}
		rows = append(rows, row)
	}
	return rows
}

// ProxyRow is one line of the Proxy board.
type ProxyRow struct {
	Provider string
	Account  string
	RefText  string
	Running  bool
	Endpoint string
	Enabled  bool
	ApiKey   string
}

// LoadProxies reads proxy-hub gateways and account-proxy status for every CanProxy provider.
func LoadProxies(ctx context.Context, a *app.App) []ProxyRow {
	var rows []ProxyRow

	if a != nil && a.ProxyHub != nil {
		configs, err := a.ProxyHub.ListConfigsContext(ctx)
		if err != nil || len(configs) == 0 {
			configs = []hub.Config{{Name: "default", Enabled: true, Port: 4680}}
		}
		for _, cfg := range configs {
			pidFile := filepath.Join(a.Root, "runtime", "hub", cfg.Name, "service.pid")
			rec := ownership.Load(pidFile)
			running := rec != nil && rec.VerifyLive()
			port := cfg.Port
			if port == 0 {
				port = 4680
			}
			endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
			rows = append(rows, ProxyRow{
				Provider: "proxy-hub",
				Account:  cfg.Name,
				RefText:  "proxy-hub/" + cfg.Name,
				Running:  running,
				Enabled:  cfg.Enabled,
				Endpoint: endpoint,
			})
		}
	}

	if a != nil && a.Providers != nil {
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
	}
	return rows
}
