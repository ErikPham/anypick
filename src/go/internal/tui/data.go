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
	"github.com/outof0/anypick/go/internal/proxy"
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

		accounts, listErr := a.Store.ListAccountsContext(ctx, id)
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
	accounts, err := a.Store.ListAccountsContext(ctx, providerID)
	if err != nil {
		return nil, err
	}
	active, _ := a.Store.GetActiveContext(ctx, providerID)

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
