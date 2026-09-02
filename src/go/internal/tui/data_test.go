package tui

import (
	"context"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/proxy"
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
	// divide by zero or index out of range with a zero width/height.
	m := New(context.Background(), nil)
	m.providers = []ProviderRow{{ID: "codex", DisplayName: "OpenAI Codex", Identity: "signed out"}}
	if got := m.View(); got == "" {
		t.Error("View() = empty before the first window-size message")
	}

	m.screen = screenAccounts
	m.accounts = []AccountRow{{ProviderID: "codex", Name: "work"}}
	if got := m.View(); got == "" {
		t.Error("View() = empty on the accounts screen")
	}
}
