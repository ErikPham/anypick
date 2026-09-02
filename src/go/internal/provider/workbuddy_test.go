package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

func TestWorkBuddyProviderShape(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	if p.ID() != "workbuddy" {
		t.Fatalf("ID = %q, want workbuddy", p.ID())
	}
	if p.Name() != "WorkBuddy AI" {
		t.Fatalf("Name = %q", p.Name())
	}
	if !p.CanProxy() {
		t.Fatal("CanProxy = false, want true")
	}
	if p.ShortName() != "WorkBuddy" {
		t.Fatalf("ShortName = %q", p.ShortName())
	}
}

func TestWorkBuddyAccountAdapterTransport(t *testing.T) {
	a := transport.WorkBuddyAccountAdapter("workbuddy", "main")
	cases := map[string]transport.Capability{
		"workbuddy": transport.Direct,
		"claude":    transport.ManagedBuiltinProxy,
		"codex":     transport.ManagedBuiltinProxy,
		"opencode":  transport.ManagedBuiltinProxy,
		"gemini":    transport.Unsupported,
		"kiro":      transport.Unsupported,
	}
	for client, want := range cases {
		if got := a.TransportFor(client); got != want {
			t.Fatalf("WorkBuddyAccountAdapter.TransportFor(%q) = %v, want %v", client, got, want)
		}
	}
}

func TestWorkBuddyLoginMethods(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	methods := p.LoginMethods()
	if len(methods) != 2 {
		t.Fatalf("LoginMethods = %+v, want two sites", methods)
	}
	preferred := 0
	for _, m := range methods {
		// No launcher binary is involved, so every method is available.
		if !m.Available {
			t.Fatalf("method %q not available: %s", m.ID, m.UnavailableReason)
		}
		// These endpoints are undocumented, so claiming "official" would be
		// telling users Tencent supports this.
		if m.Provenance != "community" {
			t.Fatalf("method %q provenance = %q, want community", m.ID, m.Provenance)
		}
		if m.Preferred {
			preferred++
		}
	}
	if preferred != 1 {
		t.Fatalf("preferred methods = %d, want exactly 1", preferred)
	}
}

func TestWorkBuddyLoginRejectsUnknownMethodOrSource(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	err := p.Login(context.Background(), "wrong", "")
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("unknown method: %v", err)
	}
	err = p.Login(context.Background(), "workbuddy-international", "other")
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("foreign source: %v", err)
	}
}

// writeLiveAuth drops a token document where the provider keeps its live file.
func writeLiveAuth(t *testing.T, p *workbuddy, doc workbuddyAuthDocument) string {
	t.Helper()
	path := p.authPath()
	if err := writeWorkBuddyAuthDocument(path, doc); err != nil {
		t.Fatalf("write live auth: %v", err)
	}
	return path
}

// TestWorkBuddyAuthPathIgnoresAppConfigDir pins the staging file to one place.
// The WorkBuddy app exports WORKBUDDY_CONFIG_DIR into every child process, so
// honouring it would move AnyPick's own login state to ~/.workbuddy-ai inside
// the app and ~/.workbuddy outside it — the same account would read as logged
// in or not depending on where the command was typed.
func TestWorkBuddyAuthPathIgnoresAppConfigDir(t *testing.T) {
	home := t.TempDir()
	p := newWorkBuddy(home)
	want := filepath.Join(home, ".workbuddy", workbuddyAuthFile)

	t.Setenv("WORKBUDDY_CONFIG_DIR", filepath.Join(home, "elsewhere"))
	t.Setenv("CODEBUDDY_CONFIG_DIR", filepath.Join(home, "elsewhere"))
	if got := p.authPath(); got != want {
		t.Fatalf("authPath = %q, want %q", got, want)
	}

	// The explicit escape hatch still wins, and it is the only thing that
	// moves the file.
	override := filepath.Join(home, "explicit", "auth.json")
	t.Setenv("WORKBUDDY_AUTH_PATH", override)
	if got := p.authPath(); got != override {
		t.Fatalf("authPath with override = %q, want %q", got, override)
	}
}

func TestWorkBuddyDetectLiveAbsentOnGarbage(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	// A file AnyPick did not write is not a login, and must not be an error.
	if err := os.MkdirAll(filepath.Dir(p.authPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.authPath(), []byte(`{"email":"a@b.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil {
		t.Fatalf("DetectLive on foreign file: %v", err)
	}
	if st.Present {
		t.Fatal("DetectLive = present for a document with no access token")
	}
}

func TestWorkBuddyBackupRestoreDetectLive(t *testing.T) {
	home := t.TempDir()
	p := newWorkBuddy(home)

	if st, err := p.DetectLive(); err != nil || st.Present {
		t.Fatalf("DetectLive before login: present=%v err=%v", st.Present, err)
	}
	if _, err := p.Backup(t.TempDir()); err == nil {
		t.Fatal("Backup should fail with no live auth")
	}

	writeLiveAuth(t, p, workbuddyAuthDocument{
		Version:      workbuddyAuthVersion,
		Kind:         workbuddyKindSub,
		MethodID:     "workbuddy-international",
		Base:         "https://www.codebuddy.ai",
		Domain:       "www.codebuddy.ai",
		AccessToken:  "at-123",
		RefreshToken: "rt-456",
		UserID:       "user-9",
	})

	st, err := p.DetectLive()
	if err != nil || !st.Present {
		t.Fatalf("DetectLive after login: present=%v err=%v", st.Present, err)
	}
	if st.Identity != "user-9" {
		t.Fatalf("DetectLive identity = %q, want user-9", st.Identity)
	}

	dest := t.TempDir()
	info, err := p.Backup(dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.Identity != "user-9" {
		t.Fatalf("Backup identity = %q, want user-9", info.Identity)
	}
	// proxy-only keeps core from restoring a file nobody reads back into
	// ~/.workbuddy; external_oauth records how the token was obtained.
	if info.CredentialKind != "proxy-only" || info.CredentialClass != "external_oauth" {
		t.Fatalf("Backup kind/class = %q/%q, want proxy-only/external_oauth",
			info.CredentialKind, info.CredentialClass)
	}
	if _, err := os.Stat(filepath.Join(dest, workbuddyAuthFile)); err != nil {
		t.Fatalf("backup file not written: %v", err)
	}

	p2 := newWorkBuddy(t.TempDir())
	if err := p2.Restore(dest); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if st, err := p2.DetectLive(); err != nil || !st.Present || st.Identity != "user-9" {
		t.Fatalf("DetectLive after restore: %+v err=%v", st, err)
	}

	desc, err := p2.DescribeSnapshot(dest)
	if err != nil {
		t.Fatalf("DescribeSnapshot: %v", err)
	}
	if desc.Identity != "user-9" || desc.CredentialKind != "proxy-only" {
		t.Fatalf("DescribeSnapshot = %+v", desc)
	}
}

// TestWorkBuddyResolveUpstreamFollowsIssuingHost pins the fix for the wrong
// endpoint: a token from one Tencent site is rejected by the other, so the
// host has to come from the credential rather than a package default.
func TestWorkBuddyResolveUpstreamFollowsIssuingHost(t *testing.T) {
	cases := map[string]string{
		"https://www.codebuddy.ai":    "https://www.codebuddy.ai/v2",
		"https://copilot.tencent.com": "https://copilot.tencent.com/v2",
	}
	for base, want := range cases {
		p := newWorkBuddy(t.TempDir())
		snap := t.TempDir()
		if err := writeWorkBuddyAuthDocument(filepath.Join(snap, workbuddyAuthFile), workbuddyAuthDocument{
			Version:     workbuddyAuthVersion,
			Kind:        workbuddyKindSub,
			Base:        base,
			AccessToken: "at-123",
			UserID:      "user-9",
		}); err != nil {
			t.Fatal(err)
		}
		up, err := p.ResolveUpstream(snap)
		if err != nil {
			t.Fatalf("ResolveUpstream(%s): %v", base, err)
		}
		if up.BaseURL.String() != want {
			t.Fatalf("base = %v, want %s", up.BaseURL, want)
		}
		if up.Auth != "at-123" || up.AuthHeader != "Authorization" || up.AuthValuePrefix != "Bearer " {
			t.Fatalf("upstream auth = %q %q %q", up.Auth, up.AuthHeader, up.AuthValuePrefix)
		}
		if !up.TranslateAnthropic {
			t.Fatal("TranslateAnthropic = false, want true for Anthropic-speaking clients")
		}
	}
}

func TestWorkBuddyResolveUpstreamOverride(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	snap := t.TempDir()
	if err := writeWorkBuddyAuthDocument(filepath.Join(snap, workbuddyAuthFile), workbuddyAuthDocument{
		Version:     workbuddyAuthVersion,
		Kind:        workbuddyKindSub,
		Base:        "https://www.codebuddy.ai",
		AccessToken: "at-123",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_BASE_URL", "https://example.com/v1")
	up, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatalf("ResolveUpstream (override): %v", err)
	}
	if up.BaseURL.String() != "https://example.com/v1" {
		t.Fatalf("override base = %v, want https://example.com/v1", up.BaseURL)
	}
}

func TestWorkBuddyResolveUpstreamNoCredential(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	if _, err := p.ResolveUpstream(t.TempDir()); err == nil {
		t.Fatal("ResolveUpstream should fail without a credential")
	}
}

func TestWorkBuddyClearLive(t *testing.T) {
	p := newWorkBuddy(t.TempDir())
	// Clearing a login that is not there is not an error.
	if err := p.ClearLive(); err != nil {
		t.Fatalf("ClearLive on empty: %v", err)
	}
	writeLiveAuth(t, p, workbuddyAuthDocument{
		Version: workbuddyAuthVersion, Kind: workbuddyKindSub, AccessToken: "at-123",
	})
	if err := p.ClearLive(); err != nil {
		t.Fatalf("ClearLive: %v", err)
	}
	if st, err := p.DetectLive(); err != nil || st.Present {
		t.Fatalf("DetectLive after ClearLive: present=%v err=%v", st.Present, err)
	}
}

func TestWorkBuddyTokenSubject(t *testing.T) {
	claims, err := json.Marshal(map[string]string{"sub": "user-42"})
	if err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	if got := workbuddyTokenSubject(token); got != "user-42" {
		t.Fatalf("workbuddyTokenSubject = %q, want user-42", got)
	}
	for _, bad := range []string{"", "not-a-jwt", "a.b", "a.b.c"} {
		if got := workbuddyTokenSubject(bad); got != "" {
			t.Fatalf("workbuddyTokenSubject(%q) = %q, want empty", bad, got)
		}
	}
}

func TestOpenBrowserURLRejectsNonHTTPS(t *testing.T) {
	for _, raw := range []string{"http://example.com", "file:///etc/passwd", ""} {
		if err := openBrowserURL(raw); err == nil {
			t.Fatalf("openBrowserURL(%q) accepted", raw)
		}
	}
}
