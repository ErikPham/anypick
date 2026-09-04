package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/proxy"
)

// anyerrCode extracts the stable machine code from an anyerr.Error.
func anyerrCode(err error) string {
	var ae *anyerr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// writeGrokAuth writes a single-session auth.json into dir and returns its
// path. entry is the raw JSON object stored under the storageKey.
func writeGrokAuth(t *testing.T, dir, storageKey, entry string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	b, _ := json.Marshal(storageKey)
	body := "{" + string(b) + ":" + entry + "}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGrokResolveUpstreamNoAuth asserts a missing snapshot auth.json fails
// fast with NO_LIVE_AUTH (ports the TS "Grok auth not found" error).
func TestGrokResolveUpstreamNoAuth(t *testing.T) {
	dir := t.TempDir()
	up, err := grokUpstream(filepath.Join(dir, "auth.json"))
	if err == nil {
		t.Fatalf("expected error, got upstream %v", up)
	}
	if code := anyerrCode(err); code != "NO_LIVE_AUTH" {
		t.Fatalf("code = %q, want NO_LIVE_AUTH", code)
	}
}

// TestGrokResolveUpstreamNoToken fails fast when the session has neither an
// access nor a refresh token (ports "Grok session has no access or refresh
// token.").
func TestGrokResolveUpstreamNoToken(t *testing.T) {
	dir := t.TempDir()
	path := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)
	up, err := grokUpstream(path)
	if err == nil {
		t.Fatalf("expected error, got upstream %v", up)
	}
	if code := anyerrCode(err); code != "NO_LIVE_AUTH" {
		t.Fatalf("code = %q, want NO_LIVE_AUTH", code)
	}
}

// TestGrokResolveUpstreamShape asserts the returned Upstream points at the
// Grok base on both surfaces, carries a per-request AuthProvider, and the
// three vendor headers.
func TestGrokResolveUpstreamShape(t *testing.T) {
	dir := t.TempDir()
	path := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)
	up, err := grokUpstream(path)
	if err != nil {
		t.Fatalf("grokUpstream: %v", err)
	}
	if up.BaseURL.String() != grokDefaultUpstream {
		t.Errorf("BaseURL = %q, want %q", up.BaseURL.String(), grokDefaultUpstream)
	}
	if up.AuthProvider == nil {
		t.Error("AuthProvider must be set (token refresh)")
	}
	if up.Anthropic == nil || up.Anthropic.BaseURL.String() != grokDefaultUpstream {
		t.Errorf("Anthropic base = %v, want %q", up.Anthropic, grokDefaultUpstream)
	}
	want := map[string]string{
		"x-grok-client-version":    grokClientVersion,
		"x-grok-client-identifier": "anypick-grok-proxy",
		"user-agent":               "anypick-grok-proxy/" + grokClientVersion,
	}
	if len(up.ExtraHeaders) != len(want) {
		t.Fatalf("ExtraHeaders = %d, want %d", len(up.ExtraHeaders), len(want))
	}
	for _, h := range up.ExtraHeaders {
		if want[h.Name] != h.Value {
			t.Errorf("header %q = %q, want %q", h.Name, h.Value, want[h.Name])
		}
	}
}

// TestGrokEnsureAccessTokenReadsSnapshot asserts the AuthProvider pulls the
// live token from the saved auth.json (no expiry when expires_at absent).
func TestGrokEnsureAccessTokenReadsSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)
	tok, err := grokEnsureAccessToken(path)
	if err != nil {
		t.Fatalf("grokEnsureAccessToken: %v", err)
	}
	if tok != "sk-live" {
		t.Errorf("token = %q, want sk-live", tok)
	}
}

// TestGrokLoadSessionPicksOIDC asserts pickGrokSession prefers an OIDC-looking
// entry over a first-but-non-OIDC entry.
func TestGrokLoadSessionPicksOIDC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	doc := `{"first::nope":{"oidc_issuer":"https://auth.x.ai","oidc_client_id":"nope"},` +
		`"https://auth.x.ai::oidc":{"auth_mode":"oidc","refresh_token":"rt","oidc_issuer":"https://auth.x.ai","oidc_client_id":"oidc"}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadGrokSession(path)
	if err != nil {
		t.Fatalf("loadGrokSession: %v", err)
	}
	if s.storageKey != "https://auth.x.ai::oidc" {
		t.Errorf("storageKey = %q, want the OIDC entry", s.storageKey)
	}
}

// TestGrokProxyInjectsAuthAndHeaders drives the real proxy Server (the path
// grok uses) end to end, guarding upstreamValue() propagation of AuthProvider
// + ExtraHeaders. The token is read from the snapshot; both OpenAI and native
// Anthropic surfaces get the Bearer token and the three vendor headers.
func TestGrokProxyInjectsAuthAndHeaders(t *testing.T) {
	dir := t.TempDir()
	authPath := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)

	type capture struct {
		auth, version, identifier, ua string
	}
	var rec struct {
		openAI    capture
		anthropic capture
	}
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c *capture
		if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			c = &rec.anthropic
		} else {
			c = &rec.openAI
		}
		c.auth = r.Header.Get("Authorization")
		c.version = r.Header.Get("x-grok-client-version")
		c.identifier = r.Header.Get("x-grok-client-identifier")
		c.ua = r.Header.Get("user-agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upSrv.Close()

	base, err := url.Parse(upSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Build the upstream through the real wiring (grokUpstream) so the test
	// catches regressions there — e.g. a forgotten AuthProvider.
	up, err := grokUpstream(authPath)
	if err != nil {
		t.Fatalf("grokUpstream: %v", err)
	}
	up.BaseURL = base
	up.Anthropic.BaseURL = base
	srv := &proxy.Server{
		ProviderID:              "grok",
		AccountName:             "test",
		InstanceID:              "inst",
		Token:                   "secret",
		Upstream:                up.BaseURL,
		UpstreamAuthHeader:      up.AuthHeader,
		UpstreamAuthValuePrefix: up.AuthValuePrefix,
		UpstreamAuthProvider:    up.AuthProvider,
		UpstreamExtraHeaders:    up.ExtraHeaders,
		Anthropic:               up.Anthropic,
	}
	if err := srv.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := srv.WaitReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	endpoint := srv.Endpoint

	check := func(path string, got *capture) {
		req, _ := http.NewRequest(http.MethodGet, endpoint+path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if got.auth != "Bearer sk-live" {
			t.Errorf("%s authorization = %q, want Bearer sk-live", path, got.auth)
		}
		if got.version != grokClientVersion {
			t.Errorf("%s x-grok-client-version = %q, want %q", path, got.version, grokClientVersion)
		}
		if got.identifier != "anypick-grok-proxy" {
			t.Errorf("%s x-grok-client-identifier = %q, want anypick-grok-proxy", path, got.identifier)
		}
		if !strings.HasPrefix(got.ua, "anypick-grok-proxy/") {
			t.Errorf("%s user-agent = %q, want prefix anypick-grok-proxy/", path, got.ua)
		}
	}
	check("/v1/models", &rec.openAI)
	check("/v1/messages", &rec.anthropic)
}
