package tui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

func TestDeriveLiveRelation(t *testing.T) {
	tests := []struct {
		name string
		in   relationInput
		want Relation
	}{
		{
			name: "no live and nothing saved",
			in:   relationInput{savedCount: 0, livePresent: false},
			want: RelationEmpty,
		},
		{
			name: "signed out with saved accounts",
			in:   relationInput{savedCount: 3, livePresent: false},
			want: RelationNoLive,
		},
		{
			name: "live identity equals the active account",
			in: relationInput{
				savedCount: 2, livePresent: true, liveIdentity: "a@example.com",
				activeName: "work", activeIdentity: "A@example.com",
				savedIdentities: []string{"a@example.com", "b@example.com"},
			},
			want: RelationMatch,
		},
		{
			// Drift means the live login is a *different saved* account, not an
			// unknown one: it must match something saved but not the active row.
			name: "live matches another saved account, not the active one",
			in: relationInput{
				savedCount: 2, livePresent: true, liveIdentity: "live@example.com",
				activeName: "work", activeIdentity: "saved@example.com",
				savedIdentities: []string{"saved@example.com", "live@example.com"},
			},
			want: RelationDrift,
		},
		{
			// A live login that matches nothing saved is unsaved, even when an
			// account is active. Reporting "drift" here would send the user to
			// fix a mismatch instead of saving a new login.
			name: "live matches no saved account although one is active",
			in: relationInput{
				savedCount: 2, livePresent: true, liveIdentity: "live@example.com",
				activeName: "work", activeIdentity: "saved@example.com",
				savedIdentities: []string{"saved@example.com", "other@example.com"},
			},
			want: RelationUnsaved,
		},
		{
			name: "live login matches no saved account",
			in: relationInput{
				savedCount: 2, livePresent: true, liveIdentity: "live@example.com",
				savedIdentities: []string{"a@example.com"},
			},
			want: RelationUnsaved,
		},
		{
			name: "missing identities never claim drift",
			in: relationInput{
				savedCount: 1, livePresent: true, liveIdentity: "",
				activeName: "work", activeIdentity: "",
				savedIdentities: []string{""},
			},
			want: RelationUnknown,
		},
		{
			name: "a status read failure wins",
			in:   relationInput{savedCount: 2, livePresent: true, err: true},
			want: RelationError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveLiveRelation(tc.in); got != tc.want {
				t.Errorf("deriveLiveRelation() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRelationHintCoversEveryRelation(t *testing.T) {
	// A new relation variant with no hint would render a bare row with no
	// explanation, so every variant must be handled.
	for _, r := range []Relation{
		RelationMatch, RelationDrift, RelationUnsaved,
		RelationNoLive, RelationEmpty, RelationUnknown, RelationError,
	} {
		if r == RelationUnknown {
			continue // unknown deliberately renders nothing
		}
		if r.hint() == "" && r != RelationMatch {
			t.Errorf("relation %q has no hint", r)
		}
	}
}

func TestIdentitiesMatchIsThreeValued(t *testing.T) {
	if got := identitiesMatch("a@x.com", "A@X.com"); got == nil || !*got {
		t.Error("case-insensitive identities must match")
	}
	if got := identitiesMatch("a@x.com", "b@x.com"); got == nil || *got {
		t.Error("different identities must not match")
	}
	if got := identitiesMatch("", "b@x.com"); got != nil {
		t.Error("a missing identity must be unknown, not false")
	}
}

func TestFormatProxyLabel(t *testing.T) {
	tests := []struct {
		name string
		st   *proxy.Status
		want string
	}{
		{"nil status", nil, ""},
		{"running with a port", &proxy.Status{Running: true, Port: 4148}, "proxy :4148"},
		{"running with only an endpoint", &proxy.Status{Running: true, Endpoint: "http://127.0.0.1:9999"}, "proxy 127.0.0.1:9999"},
		{"enabled but stopped", &proxy.Status{Enabled: true}, "proxy stopped"},
		{
			"an unavailable reason is shown verbatim",
			&proxy.Status{Enabled: true, Detail: "kirolink missing"},
			"kirolink missing",
		},
		{"disabled", &proxy.Status{}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatProxyLabel(tc.st); got != tc.want {
				t.Errorf("formatProxyLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIdentityDisplayTextHidesCredentialFingerprints(t *testing.T) {
	if got := identityDisplayText("antigravity:0123456789abcdef", "—"); got != "Gemini OAuth" {
		t.Errorf("fingerprint = %q, want Gemini OAuth", got)
	}
	if got := identityDisplayText("", "signed out"); got != "signed out" {
		t.Errorf("empty identity = %q, want the fallback", got)
	}
	if got := identityDisplayText("user@example.com", "—"); got != "user@example.com" {
		t.Errorf("plain identity = %q, want it unchanged", got)
	}
}

func TestSuggestAccountName(t *testing.T) {
	tests := []struct {
		identity string
		want     string
	}{
		{"", "main"},
		{"user@example.com", "user"},
		{"nobody", "nobody"},
		{"  spaced@example.com  ", "spaced"},
	}
	for _, tc := range tests {
		if got := suggestAccountName(tc.identity); got != tc.want {
			t.Errorf("suggestAccountName(%q) = %q, want %q", tc.identity, got, tc.want)
		}
	}
}

func TestAccountDisplayNamePrefersALabel(t *testing.T) {
	withLabel := AccountRow{Name: "work", Label: "Personal"}
	if got := withLabel.DisplayName(); got != "Personal" {
		t.Errorf("DisplayName() = %q, want the label", got)
	}
	// Path-like labels are an import artifact and must never be shown.
	pathLike := AccountRow{Name: "work", Label: "Live ~/.codex/auth.json"}
	if got := pathLike.DisplayName(); got != "work" {
		t.Errorf("DisplayName() = %q, want the account name, not a path", got)
	}
	bare := AccountRow{Name: "work"}
	if got := bare.DisplayName(); got != "work" {
		t.Errorf("DisplayName() = %q, want work", got)
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		iso  string
		want string
	}{
		{now.Add(-5 * time.Second).Format(time.RFC3339), "just now"},
		{now.Add(-10 * time.Minute).Format(time.RFC3339), "10m ago"},
		{now.Add(-5 * time.Hour).Format(time.RFC3339), "5h ago"},
		{now.Add(-50 * time.Hour).Format(time.RFC3339), "2d ago"},
		{now.Add(-90 * 24 * time.Hour).Format(time.RFC3339), "3mo ago"},
		{"not-a-date", "not-a-date"},
	}
	for _, tc := range tests {
		if got := formatRelativeTime(tc.iso, now); got != tc.want {
			t.Errorf("formatRelativeTime(%q) = %q, want %q", tc.iso, got, tc.want)
		}
	}
}

func TestTruncateRespectsRunesNotBytes(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string = %q, want unchanged", got)
	}
	// A multi-byte name must not be cut mid-rune.
	got := truncate("日本語テストです", 5)
	if len([]rune(got)) != 5 {
		t.Errorf("truncate() = %q, want 5 runes", got)
	}
}

func TestViewRendersWithoutAWindowSize(t *testing.T) {
	// The first paint happens before WindowSizeMsg arrives, so View must not
	// divide by zero or index out of range with a zero width/height. The Apps
	// board needs a real *app.App, so this guard exercises the Accounts board
	// (drilled) which only reads cached account rows.
	m := New(context.Background(), nil)
	m.board = boardAccounts
	m.accountDrill = true
	m.accounts = []AccountRow{{ProviderID: "codex", Name: "work"}}
	if got := m.View(); got == "" {
		t.Error("View() = empty on the accounts board before the first window-size message")
	}
}

// TestEveryRegisteredClientIsReachableInTheTUI fails when a registered client
// can be reached from nowhere in the terminal UI.
//
// The Apps board enumerates clients through routingAppIDs only — it is the
// single place the TUI lists them — so a client absent from that allowlist is
// reachable solely by being a provider AnyPick logs in for, which is how
// gemini/kiro/opencode/workbuddy work under Accounts. zcode is neither: it
// shipped registered as a client, usable from the CLI and the desktop app, and
// invisible here. Pick one of the two surfaces when adding a client.
func TestEveryRegisteredClientIsReachableInTheTUI(t *testing.T) {
	a := &app.App{Providers: provider.NewRegistry(), Clients: client.NewRegistry()}
	if err := provider.RegisterBuiltin(a.Providers, t.TempDir()); err != nil {
		t.Fatalf("RegisterBuiltin(providers): %v", err)
	}
	if err := provider.RegisterBuiltinCompatibles(a.Providers, t.TempDir()); err != nil {
		t.Fatalf("RegisterBuiltinCompatibles: %v", err)
	}
	if err := client.RegisterBuiltin(a.Clients, t.TempDir()); err != nil {
		t.Fatalf("RegisterBuiltin(clients): %v", err)
	}

	routable := make(map[string]bool, len(routingAppAllowlist))
	for _, id := range routingAppAllowlist {
		routable[id] = true
	}

	ids := a.Clients.IDs()
	sort.Strings(ids)
	if len(ids) == 0 {
		t.Fatal("the client registry registered nothing; the check below would pass vacuously")
	}
	for _, id := range ids {
		if routable[id] || a.Providers.Has(id) {
			continue
		}
		t.Errorf("client %q is invisible in the TUI: it is not in routingAppAllowlist "+
			"and no provider named %q exists to switch its login under Accounts — "+
			"either add it to routingAppAllowlist or give it a provider", id, id)
	}
}

// TestClientNativeProviderResolvesRegisteredApps is the regression probe for
// the catalog-id/provider-id confusion. clientNativeProvider used to look up a
// hand-kept map of catalog ids ("anthropic", "openai") in the provider
// registry, which registers provider ids, so the guard was false for every
// client and the Apps board never showed "own login".
func TestClientNativeProviderResolvesRegisteredApps(t *testing.T) {
	a, cleanup := newTUIApp(t)
	defer cleanup()

	for clientID, want := range map[string]string{
		"claude": "claude",
		"codex":  "codex",
	} {
		if got := clientNativeProvider(a, clientID); got != want {
			t.Errorf("clientNativeProvider(%q) = %q, want %q", clientID, got, want)
		}
	}
}

// TestClientNativeProviderIsDerivedFromTheRegistry iterates the live client
// registry instead of restating a client list, so a newly registered client is
// covered without anyone remembering to add it here. The expectation is itself
// derived from the provider registry, which is what pins the non-identity case:
// zai owns the zcode login, so "the client id is a provider id" is not the rule.
func TestClientNativeProviderIsDerivedFromTheRegistry(t *testing.T) {
	a, cleanup := newTUIApp(t)
	defer cleanup()

	want := map[string]string{}
	for _, providerID := range a.Providers.IDs() {
		pv, err := a.Providers.Get(providerID)
		if err != nil {
			t.Fatalf("Providers.Get(%q): %v", providerID, err)
		}
		native := providerID
		if ncp, ok := pv.(provider.NativeClientProvider); ok && strings.TrimSpace(ncp.NativeClientID()) != "" {
			native = strings.TrimSpace(ncp.NativeClientID())
		}
		if a.Clients.Has(native) {
			if prev, dup := want[native]; dup {
				t.Fatalf("client %q is claimed as the native login by both %q and %q; "+
					"the lookup can only answer one", native, prev, providerID)
			}
			want[native] = providerID
		}
	}
	if len(want) == 0 {
		t.Fatal("no registered client has a provider; this check would pass vacuously")
	}

	ids := a.Clients.IDs()
	if len(ids) == 0 {
		t.Fatal("the client registry registered nothing; this check would pass vacuously")
	}
	for _, clientID := range ids {
		if got := clientNativeProvider(a, clientID); got != want[clientID] {
			t.Errorf("clientNativeProvider(%q) = %q, want %q", clientID, got, want[clientID])
		}
	}
	if got := clientNativeProvider(a, "zcode"); got != "zai" {
		t.Errorf("clientNativeProvider(%q) = %q, want zai: the relationship is not the identity", "zcode", got)
	}
}

// TestLoadAppsReachesAppNative covers the status the defect made unreachable.
// Before the fix every routing app fell through to "not connected", so no test
// could reach AppNative at all.
func TestLoadAppsReachesAppNative(t *testing.T) {
	a, cleanup := newTUIApp(t)
	defer cleanup()
	seedLiveClaude(t)

	rows := LoadApps(context.Background(), a)
	i := indexOfApp(rows, "claude")
	if i < 0 {
		t.Fatal("claude is not on the Apps board")
	}
	if rows[i].Status != AppNative {
		t.Fatalf("claude status = %q, want %q (source %q)", rows[i].Status, AppNative, rows[i].SourceText)
	}
	if rows[i].SourceText != "own login" {
		t.Errorf("claude source = %q, want %q", rows[i].SourceText, "own login")
	}
}

// TestLoadSourceOptionsGroupsNativeAccounts asserts the wizard's first step
// offers an app's own saved logins under "Native accounts". The group was
// always empty for the apps the docs promise it for, because the native
// provider resolved to "" and the branch was skipped.
func TestLoadSourceOptionsGroupsNativeAccounts(t *testing.T) {
	a, cleanup := newTUIApp(t)
	defer cleanup()

	// Both apps the defect broke: their own login is the common case, and each
	// is the only routing app whose native provider is itself.
	for _, clientID := range []string{"claude", "codex"} {
		t.Run(clientID, func(t *testing.T) {
			seedSavedAccount(t, a, clientID, "work", "probe@example.com")

			opts := LoadSourceOptions(context.Background(), a, clientID)
			native := 0
			for _, o := range opts {
				if o.Group != "Native accounts" {
					continue
				}
				native++
				if o.Ref.Provider != clientID || o.Ref.Name != "work" {
					t.Errorf("Native accounts option = %s/%s, want %s/work",
						o.Ref.Provider, o.Ref.Name, clientID)
				}
			}
			if native == 0 {
				t.Fatalf("%s got no Native accounts group; groups offered: %v", clientID, sourceGroups(opts))
			}
		})
	}

	// A client no provider claims must not grow an empty group just because
	// the native lookup is now registry-derived.
	for _, o := range LoadSourceOptions(context.Background(), a, "no-such-client") {
		if o.Group == "Native accounts" {
			t.Errorf("unclaimed client got a Native accounts option: %s", refs.DisplayRef(o.Ref))
		}
	}
}

func sourceGroups(opts []SourceOption) []string {
	var groups []string
	for _, o := range opts {
		groups = append(groups, o.Group)
	}
	return groups
}

// newTUIApp builds the real composed graph against a throwaway home, so the
// builtin registries are the ones production uses and no live read can reach
// the developer's credentials.
func newTUIApp(t *testing.T) (*app.App, func()) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ANYPICK_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", filepath.Join(home, ".claude", ".credentials.json"))
	t.Setenv("CODEX_AUTH_PATH", filepath.Join(home, ".codex", "auth.json"))

	a, cleanup, err := app.Ready(context.Background(), app.Options{SkipMigrate: true})
	if err != nil {
		t.Fatalf("app.Ready: %v", err)
	}
	return a, cleanup
}

// seedLiveClaude writes a valid Claude credential to the path the isolated
// home gave the provider, making its DetectLive report a live login.
func seedLiveClaude(t *testing.T) {
	t.Helper()
	path := os.Getenv("ANYPICK_CLAUDE_CREDENTIALS_PATH")
	if path == "" {
		t.Fatal("newTUIApp did not pin ANYPICK_CLAUDE_CREDENTIALS_PATH")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"claudeAiOauth":{"email":"probe@example.com","accessToken":"at","refreshToken":"rt"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedSavedAccount saves one account through the store's prepare/write path,
// which is what a real save does.
func seedSavedAccount(t *testing.T, a *app.App, providerID, name, identity string) {
	t.Helper()
	snap, _, err := a.Store.PrepareSnapshot(providerID, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "credentials.json"), []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := store.Meta{
		Name: name, Provider: providerID, Identity: identity,
		CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	if err := a.Store.WriteMetaContext(context.Background(), meta, snap); err != nil {
		t.Fatal(err)
	}
	_ = a.Store.DiscardPreparedSnapshot(snap)
}

func TestViewProxiesOmitsApiKey(t *testing.T) {
	const hubKey = "aph_1234567890abcdef1234567890abcdef"
	const accountKey = "b3f862b6add39ed9401fdebef019006983361fd4dd8daef7ccdb5d4b8e08ba7b"
	m := New(context.Background(), nil)
	m.board = boardProxy
	m.proxies = []ProxyRow{
		{
			Provider: "proxy-hub",
			Account:  "default",
			RefText:  "proxy-hub/default",
			Running:  true,
			Endpoint: "http://127.0.0.1:4680",
			ApiKey:   hubKey,
		},
		{
			Provider: "workbuddy",
			Account:  "stacktify",
			RefText:  "workbuddy/stacktify",
			Running:  true,
			Endpoint: "http://127.0.0.1:58613",
			ApiKey:   accountKey,
		},
		{
			Provider: "codex",
			Account:  "work",
			RefText:  "codex/work",
			Running:  false,
		},
	}
	rendered := m.viewProxies()
	if len(rendered) != 3 {
		t.Fatalf("len(rendered) = %d, want 3", len(rendered))
	}
	for i, row := range rendered {
		if strings.Contains(row, hubKey) || strings.Contains(row, accountKey) || strings.Contains(row, "key ") {
			t.Errorf("row %d rendered a bearer: %q", i, row)
		}
	}
	if !strings.Contains(rendered[0], "http://127.0.0.1:4680") {
		t.Errorf("hub row dropped the endpoint: %q", rendered[0])
	}
	if !strings.Contains(rendered[1], "http://127.0.0.1:58613") {
		t.Errorf("account row dropped the endpoint: %q", rendered[1])
	}
}
