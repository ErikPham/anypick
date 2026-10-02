package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

func TestClineProviderShape(t *testing.T) {
	p := newCline(t.TempDir())
	if p.ID() != "cline" {
		t.Fatalf("ID = %q, want cline", p.ID())
	}
	if !p.CanProxy() {
		t.Fatal("CanProxy = false, want true")
	}
	if p.NativeClientID() != "cline" {
		t.Fatalf("NativeClientID = %q, want cline", p.NativeClientID())
	}
	if got := p.AccountAdapter("main", true).TransportFor("cline"); got != transport.ManagedBuiltinProxy {
		t.Fatalf("native cline pairing = %v, want managed_builtin_proxy", got)
	}
	if got := p.AccountAdapter("main", true).TransportFor("dsh"); got != transport.ManagedBuiltinProxy {
		t.Fatalf("cross-provider dsh pairing = %v, want managed_builtin_proxy", got)
	}
	if got := p.AccountAdapter("main", true).TransportFor("gemini"); got != transport.Unsupported {
		t.Fatalf("gemini pairing = %v, want unsupported", got)
	}
}

func TestClineLoginMethods(t *testing.T) {
	methods := newCline(t.TempDir()).LoginMethods()
	if len(methods) != 1 {
		t.Fatalf("LoginMethods = %+v, want exactly the ClinePass method", methods)
	}
	m := methods[0]
	if m.ID != clineLoginMethodID || !m.Preferred || !m.Available {
		t.Fatalf("method = %+v", m)
	}
	// Undocumented token reuse: never presented as official.
	if m.Provenance != "community" {
		t.Fatalf("provenance = %q, want community", m.Provenance)
	}
}

func TestClineLoginRejectsUnknownMethodOrSource(t *testing.T) {
	p := newCline(t.TempDir())
	err := p.Login(context.Background(), "wrong", "")
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("unknown method: %v", err)
	}
	err = p.Login(context.Background(), clineLoginMethodID, "other")
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("foreign source: %v", err)
	}
}

func writeClineStaging(t *testing.T, p *cline, doc clineAuthDocument) string {
	t.Helper()
	path := p.authPath()
	if err := writeClineAuthDocument(path, doc); err != nil {
		t.Fatalf("write staging: %v", err)
	}
	return path
}

// TestClineAuthPathIgnoresAppRoots pins the staging file: Cline's own roots
// move between app, CLI and fixtures, so honouring them would make one
// account read as logged in or not depending on where the command ran.
func TestClineAuthPathIgnoresAppRoots(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	want := filepath.Join(home, ".cline-anypick", clineAuthFile)
	if got := p.authPath(); got != want {
		t.Fatalf("authPath = %q, want %q", got, want)
	}
	override := filepath.Join(home, "explicit", "auth.json")
	t.Setenv("CLINE_AUTH_PATH", override)
	if got := p.authPath(); got != override {
		t.Fatalf("authPath with override = %q, want %q", got, override)
	}
}

func TestClineDetectLiveAbsentOnGarbage(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	t.Setenv("CLINE_AUTH_PATH", filepath.Join(home, "auth.json"))
	t.Setenv("CLINE_PROVIDERS_PATH", filepath.Join(home, "providers.json"))
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// Foreign files are not logins and must not error.
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"email":"a@b.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(`{"cline":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil {
		t.Fatalf("DetectLive on foreign files: %v", err)
	}
	if st.Present {
		t.Fatal("DetectLive = present for documents with no token")
	}
}

func TestClineDetectLiveReadsExternalStore(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	t.Setenv("CLINE_AUTH_PATH", filepath.Join(home, "missing", "auth.json"))
	ext := filepath.Join(home, "providers.json")
	t.Setenv("CLINE_PROVIDERS_PATH", ext)
	body := `{"providers":{"cline":{"settings":{"provider":"cline","auth":{"accessToken":"workos:at-1","refreshToken":"rt-1","expiresAt":9999999999999,"accountId":"user-7"}}}}}`
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ext, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil || !st.Present {
		t.Fatalf("DetectLive on external store: present=%v err=%v", st.Present, err)
	}
	if st.Identity != "user-7" {
		t.Fatalf("identity = %q, want user-7", st.Identity)
	}
	// Backup from the external store strips the workos: namespace prefix and
	// keeps the grant for refresh.
	dest := t.TempDir()
	info, err := p.Backup(dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.CredentialKind != "proxy-only" || info.CredentialClass != "external_oauth" {
		t.Fatalf("kind/class = %q/%q", info.CredentialKind, info.CredentialClass)
	}
	raw, _ := os.ReadFile(filepath.Join(dest, clineAuthFile))
	var doc clineAuthDocument
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("unreadable snapshot: %s", raw)
	}
	if doc.AccessToken != "workos:at-1" || doc.RefreshToken != "rt-1" {
		t.Fatalf("external tokens not carried raw: %+v", doc)
	}
}

func TestClineBearerTokenKeepsNamespace(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"workos:at-1", "workos:at-1"},
		{"WORKOS:at-1", "WORKOS:at-1"},
		{"at-1", "workos:at-1"},
		{"  at-1  ", "workos:at-1"},
	} {
		if got := clineBearerToken(tc.in); got != tc.want {
			t.Fatalf("clineBearerToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClineBackupRestoreDetectLive(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	t.Setenv("CLINE_AUTH_PATH", filepath.Join(home, "auth.json"))
	t.Setenv("CLINE_PROVIDERS_PATH", filepath.Join(home, "missing-providers.json"))

	if st, err := p.DetectLive(); err != nil || st.Present {
		t.Fatalf("DetectLive before login: present=%v err=%v", st.Present, err)
	}
	if _, err := p.Backup(t.TempDir()); err == nil {
		t.Fatal("Backup should fail with no live auth")
	}

	writeClineStaging(t, p, clineAuthDocument{
		Version: clineAuthVersion, Kind: clineKindSub, MethodID: clineLoginMethodID,
		AccessToken: "at-123", RefreshToken: "rt-456",
		ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli(), Email: "user@example.com",
	})
	if st, err := p.DetectLive(); err != nil || !st.Present || st.Identity != "user@example.com" {
		t.Fatalf("DetectLive after login: %+v err=%v", st, err)
	}
	dest := t.TempDir()
	if _, err := p.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	p2 := newCline(t.TempDir())
	if err := p2.Restore(dest); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if st, err := p2.DetectLive(); err != nil || !st.Present {
		t.Fatalf("DetectLive after restore: %+v err=%v", st, err)
	}
	if err := p.ClearLive(); err != nil {
		t.Fatalf("ClearLive: %v", err)
	}
	if st, err := p.DetectLive(); err != nil || st.Present {
		t.Fatalf("DetectLive after ClearLive: present=%v err=%v", st, err)
	}
}

// TestClineValidateSnapshotRejectsDeadGrant pins the fail-fast contract:
// an invalid_grant rejection means signing in again. Transport failures fail
// open — the vendor decides at request time.
func TestClineValidateSnapshotRejectsDeadGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false}`))
			return
		}
		t.Errorf("unexpected upstream path %s", r.URL.Path)
	}))
	defer srv.Close()
	t.Setenv("CLINE_API_BASE_URL", srv.URL+"/api/v1")

	p := newCline(t.TempDir())
	snap := t.TempDir()
	expired := time.Now().Add(-time.Hour).UnixMilli()
	if err := writeClineAuthDocument(filepath.Join(snap, clineAuthFile), clineAuthDocument{
		Version: clineAuthVersion, Kind: clineKindSub,
		AccessToken: "at-old", RefreshToken: "rt-dead", ExpiresAtMs: expired,
	}); err != nil {
		t.Fatal(err)
	}
	err := p.ValidateSnapshot(snap)
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "AUTH_REQUIRED" {
		t.Fatalf("ValidateSnapshot = %v, want AUTH_REQUIRED", err)
	}
}

// TestClineValidateSnapshotFailsOpenOffline: an unreachable service must not
// block activation with a re-login demand.
func TestClineValidateSnapshotFailsOpenOffline(t *testing.T) {
	t.Setenv("CLINE_API_BASE_URL", "http://127.0.0.1:1")
	p := newCline(t.TempDir())
	snap := t.TempDir()
	if err := writeClineAuthDocument(filepath.Join(snap, clineAuthFile), clineAuthDocument{
		Version: clineAuthVersion, Kind: clineKindSub,
		AccessToken: "at-old", RefreshToken: "rt-1",
		ExpiresAtMs: time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateSnapshot(snap); err != nil {
		t.Fatalf("offline ValidateSnapshot = %v, want nil", err)
	}
	if err := p.ValidateSnapshot(t.TempDir()); err != nil {
		t.Fatalf("empty ValidateSnapshot = %v, want nil", err)
	}
}

// TestClineStalledRefreshFailsFast pins the per-request timeout on the
// refresh path: without it a stalled connection holds activation with no
// terminal result.
func TestClineStalledRefreshFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()
	t.Setenv("CLINE_API_BASE_URL", srv.URL+"/api/v1")

	old := clineHTTPTimeout
	clineHTTPTimeout = 100 * time.Millisecond
	defer func() { clineHTTPTimeout = old }()

	start := time.Now()
	_, err := clineRefreshTokens(context.Background(), "rt-1")
	if err == nil {
		t.Fatal("stalled refresh succeeded")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("stalled refresh took %v, want a fast failure", elapsed)
	}
}

func TestClineResolveUpstreamShapesGateway(t *testing.T) {
	p := newCline(t.TempDir())
	snap := t.TempDir()
	if err := writeClineAuthDocument(filepath.Join(snap, clineAuthFile), clineAuthDocument{
		Version: clineAuthVersion, Kind: clineKindSub,
		AccessToken: "at-123", RefreshToken: "rt-456",
		ExpiresAtMs: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	up, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	if up.BaseURL.String() != clineAPIBaseDefault+"/api/v1" {
		t.Fatalf("base = %v", up.BaseURL)
	}
	if up.OpenAIPath != "/chat/completions" {
		t.Fatalf("OpenAIPath = %q", up.OpenAIPath)
	}
	if !up.TranslateAnthropic {
		t.Fatal("TranslateAnthropic = false, want true for Anthropic-speaking clients")
	}
	if up.AuthHeader != "Authorization" || up.AuthValuePrefix != "Bearer " {
		t.Fatalf("auth scheme = %q %q", up.AuthHeader, up.AuthValuePrefix)
	}
	if up.SanitizeOpenAIBody == nil || up.ModelDiscovery == nil || up.AuthProvider == nil {
		t.Fatal("upstream missing sanitizer, discovery or auth provider")
	}
	if _, err := p.ResolveUpstream(t.TempDir()); err == nil {
		t.Fatal("ResolveUpstream should fail without a credential")
	}
}

// TestClineModelDiscoveryReadsGatewayCatalog: the pass catalog comes from the
// gateway's own OpenAI list surface with a refreshed bearer — never invented.
func TestClineModelDiscoveryReadsGatewayCatalog(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k3"},{"id":"glm-5.2"}]}`))
	}))
	defer srv.Close()
	t.Setenv("CLINE_API_BASE_URL", srv.URL+"/api/v1")

	p := newCline(t.TempDir())
	snap := t.TempDir()
	future := time.Now().Add(time.Hour).UnixMilli()
	if err := writeClineAuthDocument(filepath.Join(snap, clineAuthFile), clineAuthDocument{
		Version: clineAuthVersion, Kind: clineKindSub,
		AccessToken: "at-live", RefreshToken: "rt-1", ExpiresAtMs: future,
	}); err != nil {
		t.Fatal(err)
	}
	up, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	models, err := up.ModelDiscovery(context.Background())
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if len(models) != 2 || models[0] != "kimi-k3" || models[1] != "glm-5.2" {
		t.Fatalf("models = %q", models)
	}
	if gotAuth != "Bearer workos:at-live" {
		t.Fatalf("discovery auth = %q", gotAuth)
	}
	if gotPath != "/api/v1/models" {
		t.Fatalf("discovery path = %q", gotPath)
	}
}

func TestClineRefreshPersistsRotatedGrant(t *testing.T) {
	var refreshes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/refresh" {
			http.NotFound(w, r)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh body: %v", err)
		}
		refreshes = append(refreshes, body["refreshToken"])
		if body["refreshToken"] != "rt-old" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresAt":"2099-01-01T00:00:00Z","userInfo":{"email":"c@x.dev"}}}`))
	}))
	defer srv.Close()
	t.Setenv("CLINE_API_BASE_URL", srv.URL+"/api/v1")

	p := newCline(t.TempDir())
	snap := t.TempDir()
	expired := time.Now().Add(-time.Hour).UnixMilli()
	if err := writeClineAuthDocument(filepath.Join(snap, clineAuthFile), clineAuthDocument{
		Version: clineAuthVersion, Kind: clineKindSub,
		AccessToken: "at-old", RefreshToken: "rt-old", ExpiresAtMs: expired,
		Email: "c@x.dev",
	}); err != nil {
		t.Fatal(err)
	}
	up, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	first, err := up.AuthProvider(context.Background())
	if err != nil {
		t.Fatalf("first auth: %v", err)
	}
	if first != "workos:at-new" {
		t.Fatalf("first bearer = %q", first)
	}
	second, err := up.AuthProvider(context.Background())
	if err != nil {
		t.Fatalf("second auth: %v", err)
	}
	if second != "workos:at-new" {
		t.Fatalf("second bearer = %q", second)
	}
	if len(refreshes) != 1 || refreshes[0] != "rt-old" {
		t.Fatalf("refresh calls = %v, want one exchange of rt-old", refreshes)
	}
	saved, err := readClineAuthDocument(filepath.Join(snap, clineAuthFile))
	if err != nil || saved == nil {
		t.Fatalf("reread: %+v %v", saved, err)
	}
	if saved.RefreshToken != "rt-new" || saved.AccessToken != "at-new" || saved.ExpiresAtMs == expired {
		t.Fatalf("snapshot did not keep the rotated grant: %+v", saved)
	}
}

func TestClineValidCredentialsContract(t *testing.T) {
	future := time.Now().Add(time.Hour).UnixMilli()
	cur := &clineCredentials{Access: "at-1", Refresh: "rt-1", ExpiresMs: future}
	got, err := clineValidCredentials(context.Background(), cur)
	if err != nil || got != cur {
		t.Fatalf("fresh credentials must pass through: %+v %v", got, err)
	}
	if _, err := clineValidCredentials(context.Background(), nil); err != ErrClineReauth {
		t.Fatalf("nil credentials = %v, want reauth", err)
	}
	if _, err := clineValidCredentials(context.Background(), &clineCredentials{}); err != ErrClineReauth {
		t.Fatalf("empty credentials = %v, want reauth", err)
	}
}

// The shared login is readable through the cline-pass key too: Cline merges
// the cline store into cline-pass on read.
func TestClineDetectLiveFallsBackToPassKey(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	t.Setenv("CLINE_AUTH_PATH", filepath.Join(home, "missing", "auth.json"))
	ext := filepath.Join(home, "providers.json")
	t.Setenv("CLINE_PROVIDERS_PATH", ext)
	body := `{"providers":{"cline-pass":{"settings":{"auth":{"accessToken":"at-9","accountId":"user-9"}}}}}`
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ext, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil || !st.Present || st.Identity != "user-9" {
		t.Fatalf("DetectLive via cline-pass = %+v err=%v", st, err)
	}
}

func TestClineDetectLiveExtractsEmail(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	t.Setenv("CLINE_AUTH_PATH", filepath.Join(home, "missing", "auth.json"))
	ext := filepath.Join(home, "providers.json")
	t.Setenv("CLINE_PROVIDERS_PATH", ext)

	// Case 1: metadata.userInfo.email in settings.auth
	body := `{
		"providers": {
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {
						"accessToken": "workos:at-opaque",
						"refreshToken": "rt-1",
						"expiresAt": 9999999999999,
						"accountId": "usr-123",
						"metadata": {
							"userInfo": {
								"email": "dev@example.com",
								"clineUserId": "usr-123"
							}
						}
					}
				}
			}
		}
	}`
	if err := os.WriteFile(ext, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil || !st.Present {
		t.Fatalf("DetectLive: %+v err=%v", st, err)
	}
	if st.Identity != "dev@example.com" {
		t.Fatalf("Identity = %q, want dev@example.com", st.Identity)
	}

	// Backup should carry the extracted email into the snapshot
	snapDir := t.TempDir()
	info, err := p.Backup(snapDir)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.Identity != "dev@example.com" {
		t.Fatalf("Backup info.Identity = %q, want dev@example.com", info.Identity)
	}
	desc, err := p.DescribeSnapshot(snapDir)
	if err != nil {
		t.Fatalf("DescribeSnapshot: %v", err)
	}
	if desc.Identity != "dev@example.com" {
		t.Fatalf("DescribeSnapshot Identity = %q, want dev@example.com", desc.Identity)
	}
}

func TestClineJWTEmailExtraction(t *testing.T) {
	home := t.TempDir()
	p := newCline(home)
	t.Setenv("CLINE_AUTH_PATH", filepath.Join(home, "missing", "auth.json"))
	ext := filepath.Join(home, "providers.json")
	t.Setenv("CLINE_PROVIDERS_PATH", ext)

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"jwt@example.com","sub":"u-1"}`))
	jwt := "workos:" + header + "." + payload + ".sig"

	// providers.json has no metadata or direct email, but accessToken is a JWT
	body := `{"providers":{"cline":{"settings":{"auth":{"accessToken":"` + jwt + `","accountId":"user-jwt"}}}}}`
	if err := os.WriteFile(ext, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := p.DetectLive()
	if err != nil || !st.Present {
		t.Fatalf("DetectLive: %+v err=%v", st, err)
	}
	if st.Identity != "jwt@example.com" {
		t.Fatalf("Identity = %q, want jwt@example.com", st.Identity)
	}

	// Also verify reading snapshot where auth.json only had accessToken (no email field)
	snapDir := t.TempDir()
	doc := clineAuthDocument{
		Version:     clineAuthVersion,
		Kind:        clineKindSub,
		AccessToken: jwt,
		AccountID:   "user-jwt",
	}
	if err := writeClineAuthDocument(filepath.Join(snapDir, clineAuthFile), doc); err != nil {
		t.Fatal(err)
	}
	desc, err := p.DescribeSnapshot(snapDir)
	if err != nil {
		t.Fatalf("DescribeSnapshot: %v", err)
	}
	if desc.Identity != "jwt@example.com" {
		t.Fatalf("DescribeSnapshot from JWT = %q, want jwt@example.com", desc.Identity)
	}
}
