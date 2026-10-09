package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// pinNoLiveOpenCodeStore keeps the live credential lookup away from the
// developer's real opencode.db: these tests describe what a saved account can
// do on a machine that has no live CLI login.
func pinNoLiveOpenCodeStore(t *testing.T) {
	t.Helper()
	t.Setenv("OPENCODE_DATA_DIR", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("XDG_DATA_HOME", "")
}

func writeOpenCodeSnapshot(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A saved mirror drives the gateway when the machine has no live login: the
// platform row decides Zen vs Go, matching the endpoint pair the compatible
// opencode-zen / opencode-go definitions declare.
func TestOpenCodeResolveUpstreamFromSnapshotMirror(t *testing.T) {
	cases := []struct {
		name      string
		mirror    string
		wantKey   string
		wantBase  string
		wantAnthr string
	}{
		{
			name:      "zen row",
			mirror:    `{"opencode":{"type":"api","key":"sk-zen"}}`,
			wantKey:   "sk-zen",
			wantBase:  "https://opencode.ai/zen/v1",
			wantAnthr: "https://opencode.ai/zen",
		},
		{
			name:      "go row targets the go gateway",
			mirror:    `{"opencode-go":{"type":"api","key":"sk-go"}}`,
			wantKey:   "sk-go",
			wantBase:  "https://opencode.ai/zen/go/v1",
			wantAnthr: "https://opencode.ai/zen/go",
		},
		{
			name:      "zen alias is not a go subscription",
			mirror:    `{"opencode-zen":{"type":"api","key":"sk-alias"}}`,
			wantKey:   "sk-alias",
			wantBase:  "https://opencode.ai/zen/v1",
			wantAnthr: "https://opencode.ai/zen",
		},
		{
			name:      "older apiKey spelling",
			mirror:    `{"opencode":{"type":"api","apiKey":"sk-alt"}}`,
			wantKey:   "sk-alt",
			wantBase:  "https://opencode.ai/zen/v1",
			wantAnthr: "https://opencode.ai/zen",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinNoLiveOpenCodeStore(t)
			snap := t.TempDir()
			writeOpenCodeSnapshot(t, snap, tc.mirror)

			up, err := newOpenCode(t.TempDir()).ResolveUpstream(snap)
			if err != nil {
				t.Fatalf("ResolveUpstream: %v", err)
			}
			if up.Auth != tc.wantKey {
				t.Errorf("Auth = %q, want %q", up.Auth, tc.wantKey)
			}
			if got := up.BaseURL.String(); got != tc.wantBase {
				t.Errorf("BaseURL = %s, want %s", got, tc.wantBase)
			}
			if up.Anthropic == nil {
				t.Fatal("Anthropic target missing: /v1/messages would not forward")
			}
			if got := up.Anthropic.BaseURL.String(); got != tc.wantAnthr {
				t.Errorf("Anthropic.BaseURL = %s, want %s", got, tc.wantAnthr)
			}
			if up.AuthHeader != "Authorization" || up.AuthValuePrefix != "Bearer " {
				t.Errorf("auth presentation = %q %q, want Authorization/Bearer",
					up.AuthHeader, up.AuthValuePrefix)
			}
		})
	}
}

// Two platform rows in one mirror must resolve deterministically: the Go
// subscription wins regardless of how the JSON object was ordered.
func TestOpenCodeResolveUpstreamPrefersGoRowInMirror(t *testing.T) {
	pinNoLiveOpenCodeStore(t)
	snap := t.TempDir()
	writeOpenCodeSnapshot(t, snap,
		`{"opencode":{"type":"api","key":"sk-zen-row"},"opencode-go":{"type":"api","key":"sk-go-row"}}`)

	for i := 0; i < 8; i++ {
		up, err := newOpenCode(t.TempDir()).ResolveUpstream(snap)
		if err != nil {
			t.Fatalf("ResolveUpstream: %v", err)
		}
		if up.Auth != "sk-go-row" {
			t.Fatalf("Auth = %q, want the go row", up.Auth)
		}
		if got := up.BaseURL.String(); got != "https://opencode.ai/zen/go/v1" {
			t.Fatalf("BaseURL = %s, want the go gateway", got)
		}
	}
}

// The beta CLI stopped rewriting auth.json on login, so a saved mirror can
// hold a previous account's key. The live credential store must win.
func TestOpenCodeResolveUpstreamPrefersLiveKeyOverMirror(t *testing.T) {
	snap := t.TempDir()
	writeOpenCodeSnapshot(t, snap, `{"opencode":{"type":"api","key":"sk-stale-mirror"}}`)
	seedLiveOpenCodeDB(t, map[string]string{"opencode-go": "sk-live-go"})

	up, err := newOpenCode(t.TempDir()).ResolveUpstream(snap)
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Auth != "sk-live-go" {
		t.Errorf("Auth = %q, want the live key", up.Auth)
	}
	if got := up.BaseURL.String(); got != "https://opencode.ai/zen/go/v1" {
		t.Errorf("BaseURL = %s, want the live key's go gateway", got)
	}
}

// The credential database that sits BESIDE a saved auth.json outranks the
// mirror. Backup never copies a database, but the step exists so a caller
// pointing at a live data directory resolves like the CLI does.
func TestOpenCodeResolveUpstreamPrefersSnapshotDBOverMirror(t *testing.T) {
	pinNoLiveOpenCodeStore(t)
	snap := t.TempDir()
	writeOpenCodeSnapshot(t, snap, `{"opencode":{"type":"api","key":"sk-mirror"}}`)
	seedOpenCodeCredentialDB(t, snap, map[string]string{"opencode-go": "sk-snapshot-db"})

	up, err := newOpenCode(t.TempDir()).ResolveUpstream(snap)
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Auth != "sk-snapshot-db" {
		t.Errorf("Auth = %q, want the database key", up.Auth)
	}
	if got := up.BaseURL.String(); got != "https://opencode.ai/zen/go/v1" {
		t.Errorf("BaseURL = %s, want the go gateway", got)
	}
}

// Service ids are matched case-insensitively.
func TestOpenCodeResolveUpstreamMatchesServiceCaseInsensitively(t *testing.T) {
	pinNoLiveOpenCodeStore(t)
	snap := t.TempDir()
	writeOpenCodeSnapshot(t, snap, `{"OpenCode-Go":{"type":"api","key":"sk-mixed-case"}}`)

	up, err := newOpenCode(t.TempDir()).ResolveUpstream(snap)
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Auth != "sk-mixed-case" {
		t.Errorf("Auth = %q, want the mixed-case row", up.Auth)
	}
	if got := up.BaseURL.String(); got != "https://opencode.ai/zen/go/v1" {
		t.Errorf("BaseURL = %s, want the go gateway", got)
	}
}

// Three different failures, three different messages — kept separate because
// they send the user to different commands.
func TestOpenCodeResolveUpstreamFailureMessages(t *testing.T) {
	cases := []struct {
		name        string
		mirror      string // empty → no auth.json at all
		wantCode    string
		wantMessage string
	}{
		{
			name:        "no saved login",
			mirror:      "",
			wantMessage: "No OpenCode auth available.",
		},
		{
			name:        "unreadable mirror",
			mirror:      `{`,
			wantMessage: "Unreadable OpenCode auth:",
		},
		{
			name:   "oauth login only",
			mirror: `{"openai":{"type":"oauth","access":"` + fakeJWT(map[string]string{"email": "oc@openai"}) + `","refresh":"r"}}`,
			// The message names the missing Zen/Go key, not a generic failure.
			wantMessage: "needs a Zen/Go API key",
		},
		{
			name:        "third-party api key only",
			mirror:      `{"deepseek":{"type":"api","key":"sk-deepseek"}}`,
			wantMessage: "needs a Zen/Go API key",
		},
		{
			name:        "platform row without a key",
			mirror:      `{"opencode":{"type":"api"}}`,
			wantMessage: "needs a Zen/Go API key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinNoLiveOpenCodeStore(t)
			snap := t.TempDir()
			if tc.mirror != "" {
				writeOpenCodeSnapshot(t, snap, tc.mirror)
			}

			up, err := newOpenCode(t.TempDir()).ResolveUpstream(snap)
			if err == nil {
				t.Fatalf("ResolveUpstream = %+v, want an error", up)
			}
			var coded *anyerr.Error
			if !errorsAsAnyProvider(err, &coded) {
				t.Fatalf("error = %v, want an anyerr.Error", err)
			}
			if coded.Code != "NO_LIVE_AUTH" {
				t.Errorf("code = %q, want NO_LIVE_AUTH", coded.Code)
			}
			if !strings.Contains(coded.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", coded.Message, tc.wantMessage)
			}
		})
	}
}

// The "log in first" case must actually tell the user the two commands.
func TestOpenCodeResolveUpstreamMissingAuthSuggestsLogin(t *testing.T) {
	pinNoLiveOpenCodeStore(t)
	_, err := newOpenCode(t.TempDir()).ResolveUpstream(t.TempDir())
	if err == nil {
		t.Fatal("ResolveUpstream on an empty snapshot: want an error")
	}
	var coded *anyerr.Error
	if !errorsAsAnyProvider(err, &coded) {
		t.Fatalf("error = %v, want an anyerr.Error", err)
	}
	joined := strings.Join(coded.Suggestions, " | ")
	for _, want := range []string{"opencode auth login", "anypick add opencode"} {
		if !strings.Contains(joined, want) {
			t.Errorf("suggestions = %q, want %q", joined, want)
		}
	}
}
