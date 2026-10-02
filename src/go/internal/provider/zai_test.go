package provider

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

func encryptZAIForTest(t *testing.T, secret, value string) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(value), nil)
	ciphertext := sealed[:len(sealed)-gcm.Overhead()]
	tag := sealed[len(sealed)-gcm.Overhead():]
	encode := base64.RawURLEncoding.EncodeToString
	return "enc:v1:" + encode(nonce) + "." + encode(tag) + "." + encode(ciphertext)
}

func writeZAIExternalFixture(t *testing.T, path, secret string) {
	t.Helper()
	entries := map[string]string{
		"oauth:active_provider":   encryptZAIForTest(t, secret, "zai"),
		zaiOAuthCredentialKey:     encryptZAIForTest(t, secret, "oauth-token"),
		"oauth:zai:user_info":     encryptZAIForTest(t, secret, `{"email":"zai@example.com","name":"Zai User","user_id":"user-1"}`),
		"zcodejwttoken":           encryptZAIForTest(t, secret, "jwt-token"),
		"oauth:login_attribution": encryptZAIForTest(t, secret, "zcode"),
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustNewZAI(t *testing.T, home string) *zai {
	t.Helper()
	p, err := newZAI(home)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestZAIDetectsEncryptedZCodeLoginAndStagesSnapshot(t *testing.T) {
	home := t.TempDir()
	secret := "zai-test-secret"
	t.Setenv("ZCODE_CREDENTIAL_SECRET", secret)
	externalPath := filepath.Join(home, ".zcode", "v2", "credentials.json")
	stagePath := filepath.Join(home, ".anypick", "providers", "zai", zaiLiveFile)
	t.Setenv(zaiCredentialsEnv, externalPath)
	t.Setenv(zaiLiveEnv, stagePath)
	writeZAIExternalFixture(t, externalPath, secret)

	p := mustNewZAI(t, home)
	live, err := p.DetectLive()
	if err != nil {
		t.Fatal(err)
	}
	if !live.Present || live.Identity != "zai@example.com" || live.Source != zaiSourceID {
		t.Fatalf("DetectLive = %+v", live)
	}

	snapshot := t.TempDir()
	info, err := p.Backup(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Identity != "zai@example.com" || info.CredentialKind != "proxy-only" ||
		info.CredentialClass != "external_oauth" {
		t.Fatalf("Backup info = %+v", info)
	}
	if _, err := os.Stat(filepath.Join(snapshot, zaiSnapshotFile)); err != nil {
		t.Fatal(err)
	}
	up, err := p.ResolveUpstream(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if up.BaseURL.String() != "https://api.z.ai/api/coding/paas/v4" ||
		up.Anthropic == nil || up.AuthProvider == nil {
		t.Fatalf("ResolveUpstream = %+v", up)
	}

	// The external store is read-only from AnyPick's perspective. Once it is
	// absent, a restored account is still detectable through AnyPick's staging
	// file and can be used by the proxy.
	if err := p.ClearLive(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(externalPath); err != nil {
		t.Fatal(err)
	}
	if live, err := p.DetectLive(); err != nil || live.Present {
		t.Fatalf("DetectLive after external removal = %+v, err=%v", live, err)
	}
	if err := p.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	live, err = p.DetectLive()
	if err != nil || !live.Present || live.Identity != "zai@example.com" {
		t.Fatalf("DetectLive after restore = %+v, err=%v", live, err)
	}
	if p.CanClearLive(zaiSourceID) {
		t.Fatal("external ZCode login must not be reported as safely clearable")
	}
}

func TestZAIKeepsTypedAPIKeyFallback(t *testing.T) {
	p := mustNewZAI(t, t.TempDir())
	snapshot := t.TempDir()
	if _, err := p.BackupInput("subscription-key", "sk-glm-test", "global", snapshot); err != nil {
		t.Fatal(err)
	}
	up, err := p.ResolveUpstream(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if up.BaseURL.String() != "https://api.z.ai/api/coding/paas/v4" || up.Auth != "sk-glm-test" {
		t.Fatalf("typed upstream = %+v", up)
	}
	if up.AuthProvider != nil {
		t.Fatal("typed API-key account should not need OAuth refresh")
	}
}

func TestZAIOfficialBrowserLoginUsesPollingAndBusinessExchange(t *testing.T) {
	home := t.TempDir()
	stagePath := filepath.Join(home, ".anypick", "providers", "zai", zaiLiveFile)
	t.Setenv(zaiLiveEnv, stagePath)
	var pollCount atomic.Int32
	var browserURL atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/init":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") == "" {
				http.Error(w, "bad init", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"flow_id":"flow-1","poll_token":"poll-secret","authorize_url":"https://chat.z.ai/api/oauth/authorize?state=test","expires_at":%d,"poll_interval_sec":0.01}}`, time.Now().Add(10*time.Second).Unix())
		case r.URL.Path == "/poll/flow-1":
			if r.Header.Get("Authorization") != "Bearer poll-secret" {
				http.Error(w, "wrong poll token", http.StatusUnauthorized)
				return
			}
			if pollCount.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"code":0,"data":{"status":"pending"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"status":"ready","token":"zcode-jwt","user":{"email":"browser@example.com","name":"Browser User","user_id":"browser-1"},"zai":{"access_token":"oauth-browser"}}}`)
		case r.URL.Path == "/business":
			_, _ = io.WriteString(w, `{"code":0,"data":{"access_token":"business-browser","expires_in":3600}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(zaiOAuthInitEnv, server.URL+"/init")
	t.Setenv(zaiOAuthPollEnv, server.URL+"/poll")
	t.Setenv(zaiBusinessLoginEnv, server.URL+"/business")

	oldClient := zaiOAuthHTTPClient
	oldOpen := zaiOAuthOpenBrowser
	oldTimeout := zaiOAuthPollTimeout
	defer func() {
		zaiOAuthHTTPClient = oldClient
		zaiOAuthOpenBrowser = oldOpen
		zaiOAuthPollTimeout = oldTimeout
	}()
	zaiOAuthHTTPClient = func() *http.Client { return server.Client() }
	zaiOAuthPollTimeout = 5 * time.Second
	zaiOAuthOpenBrowser = func(raw string) error {
		browserURL.Store(raw)
		return nil
	}

	if err := mustNewZAI(t, home).Login(context.Background(), zaiLoginMethodID, zaiSourceID); err != nil {
		t.Fatal(err)
	}
	opened, ok := browserURL.Load().(string)
	if !ok || opened == "" {
		t.Fatal("Z.ai browser URL was not opened")
	}
	parsed, err := url.Parse(opened)
	if err != nil || parsed.String() != "https://chat.z.ai/api/oauth/authorize?state=test" {
		t.Fatalf("browser URL = %q", opened)
	}
	live, err := mustNewZAI(t, home).DetectLive()
	if err != nil || !live.Present || live.Identity != "browser@example.com" {
		t.Fatalf("DetectLive after browser login = %+v, err=%v", live, err)
	}
	if pollCount.Load() < 2 {
		t.Fatalf("poll count = %d, want pending then ready", pollCount.Load())
	}
}

func TestZAIIsNativeCompatibleRegistration(t *testing.T) {
	reg := NewRegistry()
	home := t.TempDir()
	if err := RegisterBuiltinCompatibles(reg, home); err != nil {
		t.Fatal(err)
	}
	p, err := reg.Get("zai")
	if err != nil {
		t.Fatal(err)
	}
	methods, ok := p.(LoginMethodsProvider)
	if !ok || len(methods.LoginMethods()) != 1 || methods.LoginMethods()[0].Source != zaiSourceID {
		t.Fatalf("zai login capability = %#v", p)
	}
}

func TestZAIAccountAdapterTargetsZCode(t *testing.T) {
	p := mustNewZAI(t, t.TempDir())
	if got := p.NativeClientID(); got != "zcode" {
		t.Fatalf("NativeClientID = %q, want zcode", got)
	}
	a := p.AccountAdapter("work", false)
	if len(a.Capabilities.NativeClients) != 1 || a.Capabilities.NativeClients[0] != "zcode" {
		t.Fatalf("native clients = %v, want [zcode]", a.Capabilities.NativeClients)
	}
	if got := a.TransportFor("zcode"); got != transport.ManagedBuiltinProxy {
		t.Fatalf("zcode transport = %s, want managed builtin proxy", got)
	}
	if got := a.TransportFor("gemini"); got != transport.Unsupported {
		t.Fatalf("gemini transport = %s, want unsupported", got)
	}
}

func TestZAIEndpointOverrideRejectsRemoteHTTP(t *testing.T) {
	t.Setenv(zaiOAuthInitEnv, "http://example.com/init")
	if _, err := zaiEndpoint(zaiOAuthInitEnv, zaiOAuthInitURL); err == nil {
		t.Fatal("remote HTTP override must be rejected")
	}
	if _, err := zaiEndpoint(zaiOAuthInitEnv, zaiOAuthInitURL); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unexpected endpoint error: %v", err)
	}
}

// A server-rejected OAuth token must block activation; transport failures
// and API-key snapshots fail open.
func TestZAIValidateSnapshotRejectsDeadLogin(t *testing.T) {
	writeOAuthSnapshot := func(t *testing.T, dir string) {
		t.Helper()
		cred := zaiCredential{Version: 1, Source: zaiSourceID, OAuthAccessToken: "dead-token"}
		if err := writeZAICredential(filepath.Join(dir, zaiSnapshotFile), cred); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("server rejection fails closed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":1,"msg":"user information is invalid","data":null}`)
		}))
		t.Cleanup(srv.Close)
		t.Setenv(zaiBusinessLoginEnv, srv.URL)

		p := mustNewZAI(t, t.TempDir())
		snap := t.TempDir()
		writeOAuthSnapshot(t, snap)
		err := p.ValidateSnapshot(snap)
		var ae *anyerr.Error
		if !errors.As(err, &ae) || ae.Code != "AUTH_REQUIRED" {
			t.Fatalf("ValidateSnapshot = %v, want AUTH_REQUIRED", err)
		}
		if !strings.Contains(err.Error(), "Sign in again") {
			t.Fatalf("ValidateSnapshot = %v, want re-login hint", err)
		}
	})

	t.Run("transport failure fails open", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := srv.URL
		srv.Close()
		t.Setenv(zaiBusinessLoginEnv, deadURL)

		p := mustNewZAI(t, t.TempDir())
		snap := t.TempDir()
		writeOAuthSnapshot(t, snap)
		if err := p.ValidateSnapshot(snap); err != nil {
			t.Fatalf("ValidateSnapshot on unreachable endpoint = %v, want nil (fail open)", err)
		}
	})

	t.Run("missing snapshot fails open", func(t *testing.T) {
		t.Setenv(zaiBusinessLoginEnv, "http://127.0.0.1:9")
		if err := mustNewZAI(t, t.TempDir()).ValidateSnapshot(t.TempDir()); err != nil {
			t.Fatalf("ValidateSnapshot on empty dir = %v, want nil", err)
		}
	})

	t.Run("api-key snapshot is skipped", func(t *testing.T) {
		// An override that would fail closed proves no request is made.
		t.Setenv(zaiBusinessLoginEnv, "http://127.0.0.1:9")
		p := mustNewZAI(t, t.TempDir())
		snap := t.TempDir()
		if err := os.WriteFile(filepath.Join(snap, compatibleCredentialFile),
			[]byte(`{"version":1,"kind":"subscription-key","profile":"global","secret":"sk-test"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := p.ValidateSnapshot(snap); err != nil {
			t.Fatalf("ValidateSnapshot on api-key snapshot = %v, want nil", err)
		}
	})
}

// TestZAIAnyPickStagingLoginIsClearable: the browser flow writes AnyPick's own
// staging file. It must be reported under its own source and be clearable,
// while ZCode's external credential store stays off-limits.
func TestZAIAnyPickStagingLoginIsClearable(t *testing.T) {
	home := t.TempDir()
	stagePath := filepath.Join(home, ".anypick", "providers", "zai", zaiLiveFile)
	t.Setenv(zaiCredentialsEnv, filepath.Join(home, ".zcode", "v2", "credentials.json"))
	t.Setenv(zaiLiveEnv, stagePath)
	if err := os.MkdirAll(filepath.Dir(stagePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagePath, []byte(`{"version":1,"source":"zcode","oauthAccessToken":"staged","identity":"zai@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p := mustNewZAI(t, home)
	live, err := p.DetectLiveSource("")
	if err != nil {
		t.Fatal(err)
	}
	if !live.Present || live.Source != zaiStagingSourceID {
		t.Fatalf("DetectLiveSource = %+v, want source %q", live, zaiStagingSourceID)
	}
	if p.CanClearLive(zaiSourceID) {
		t.Fatal("ZCode's external store must never be clearable")
	}
	if !p.CanClearLive(zaiStagingSourceID) {
		t.Fatal("AnyPick's own staging login must be clearable")
	}
	if err := p.ClearLive(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stagePath); !os.IsNotExist(err) {
		t.Fatalf("staging file survived ClearLive: %v", err)
	}
}
