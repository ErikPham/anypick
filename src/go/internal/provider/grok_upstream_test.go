package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
// fast with NO_LIVE_AUTH.
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
// access nor a refresh token.
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
	if up.SanitizeOpenAIBody == nil {
		t.Error("SanitizeOpenAIBody must be set for the Grok subscription endpoint")
	}
	if up.SanitizeAnthropicBody == nil {
		t.Error("SanitizeAnthropicBody must be set: Grok strictly validates native Anthropic bodies")
	}
	if up.ModelDiscovery == nil {
		t.Error("ModelDiscovery must be set so the desktop picker and activation catalog publish grok models")
	}
	if up.MapModel == nil {
		t.Error("MapModel must be set to translate Codex native aliases")
	} else {
		if got := up.MapModel("gpt-5.6-sol"); got != "grok-4.7" {
			t.Errorf("MapModel(gpt-5.6-sol) = %q, want grok-4.7", got)
		}
		if got := up.MapModel("grok-4.5"); got != "grok-4.5" {
			t.Errorf("MapModel(grok-4.5) = %q, want grok-4.5", got)
		}
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

// TestGrokSanitizeAnthropicBodyNullRequired guards the 400 Grok returns
// for Claude Code tool schemas (`/required: null is not of type "array"`):
// a null required list means nothing is required, i.e. [].
func TestGrokSanitizeAnthropicBodyNullRequired(t *testing.T) {
	in := []byte(`{"model":"grok-4.5","max_tokens":16,"messages":[],"tools":[{"name":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":null}}]}`)
	out := grokSanitizeAnthropicBody(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("sanitized body is not JSON: %v", err)
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools lost: %s", out)
	}
	schema, _ := tools[0].(map[string]any)["input_schema"].(map[string]any)
	req, ok := schema["required"].([]any)
	if !ok || len(req) != 0 {
		t.Fatalf("required = %#v, want []: %s", schema["required"], out)
	}
	if got["model"] != "grok-4.5" {
		t.Fatalf("model rewritten: %s", out)
	}
}

func TestGrokSanitizeAnthropicBodyLeavesValidBodiesAlone(t *testing.T) {
	valid := `{"model":"grok-4.5","messages":[],"tools":[{"name":"read","input_schema":{"type":"object","required":["path"]}}]}`
	if out := grokSanitizeAnthropicBody([]byte(valid)); string(out) != valid {
		t.Fatalf("valid body rewritten:\n got %s\nwant %s", out, valid)
	}
	if out := grokSanitizeAnthropicBody([]byte(`not json`)); string(out) != `not json` {
		t.Fatalf("non-JSON rewritten: %s", out)
	}
}

// Claude Code omits `required` entirely when nothing is required; Grok reads
// the missing key as null and 400s. The sanitizer must insert [] on the
// tool schema and nested object schemas while leaving message content alone.
func TestGrokSanitizeAnthropicBodyInsertsMissingRequired(t *testing.T) {
	in := []byte(`{"model":"grok-4.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"},"filter":{"type":"object","properties":{"q":{"type":"string"}}}}}}]}`)
	out := grokSanitizeAnthropicBody(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("sanitized body is not JSON: %v", err)
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools lost: %s", out)
	}
	schema, _ := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if req, ok := schema["required"].([]any); !ok || len(req) != 0 {
		t.Fatalf("top required = %#v, want []: %s", schema["required"], out)
	}
	props, _ := schema["properties"].(map[string]any)
	// Plain string properties gain nothing.
	if _, ok := props["path"].(map[string]any)["required"]; ok {
		t.Fatalf("string property gained required: %s", out)
	}
	nested, _ := props["filter"].(map[string]any)
	if req, ok := nested["required"].([]any); !ok || len(req) != 0 {
		t.Fatalf("nested required = %#v, want []: %s", nested["required"], out)
	}
	// Message content blocks are not schemas and must not gain the key.
	msgs, _ := got["messages"].([]any)
	if _, ok := msgs[0].(map[string]any)["required"]; ok {
		t.Fatalf("message gained required: %s", out)
	}
}

func TestGrokSanitizeOpenAIBodyRemovesUnsupportedReasoningEffort(t *testing.T) {
	in := []byte(`{"model":"grok-4.20-0309-non-reasoning","reasoning_effort":"high","thinking":{"type":"enabled"},"messages":[],"tools":[{"type":"function"}]}`)
	out := grokSanitizeOpenAIBody(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("sanitized body is not JSON: %v", err)
	}
	if _, ok := got["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort was not removed")
	}
	if got["model"] != "grok-4.20-0309-non-reasoning" {
		t.Fatalf("model = %v, want unchanged model", got["model"])
	}
	if _, ok := got["thinking"]; !ok {
		t.Fatal("thinking was removed")
	}
	if _, ok := got["tools"]; !ok {
		t.Fatal("tools were removed")
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

// TestGrokModelDiscoveryServesLiveModels drives the provider-owned catalog
// against a loopback /v1/models: the desktop picker and activation catalog
// must publish the ids the subscription proxy can actually serve.
func TestGrokModelDiscoveryServesLiveModels(t *testing.T) {
	dir := t.TempDir()
	authPath := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)
	var gotAuth, gotVersion string
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("x-grok-client-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"grok-4.5"},{"id":"grok-4.3"}]}`))
	}))
	defer upSrv.Close()
	t.Setenv("GROK_MODELS_ENDPOINT", upSrv.URL)

	up, err := grokUpstream(authPath)
	if err != nil {
		t.Fatalf("grokUpstream: %v", err)
	}
	if up.ModelDiscovery == nil {
		t.Fatal("ModelDiscovery must be set")
	}
	models, err := up.ModelDiscovery(context.Background())
	if err != nil {
		t.Fatalf("ModelDiscovery: %v", err)
	}
	if len(models) != 2 || models[0] != "grok-4.5" || models[1] != "grok-4.3" {
		t.Fatalf("models = %v, want [grok-4.5 grok-4.3]", models)
	}
	if gotAuth != "Bearer sk-live" {
		t.Errorf("authorization = %q, want Bearer sk-live", gotAuth)
	}
	if gotVersion != grokClientVersion {
		t.Errorf("x-grok-client-version = %q, want %q", gotVersion, grokClientVersion)
	}
	// The same operation backs proxy.DiscoverModels, so GET /v1/models on the
	// local proxy publishes these ids instead of ErrModelDiscoveryUnsupported.
	got, err := proxy.DiscoverModels(context.Background(), up)
	if err != nil {
		t.Fatalf("proxy.DiscoverModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("proxy models = %v, want 2 ids", got)
	}
}

// TestGrokModelDiscoveryFallsBackToStatic keeps the picker usable when the
// live endpoint is unreachable: the vendor-published grok-api list is what
// the proxy forwards verbatim, so it is safe to publish (and matches the
// `anypick models` CLI fallback).
func TestGrokModelDiscoveryFallsBackToStatic(t *testing.T) {
	dir := t.TempDir()
	authPath := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)
	// Loopback server that answers 500: live fails, static must win.
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upSrv.Close()
	t.Setenv("GROK_MODELS_ENDPOINT", upSrv.URL)

	up, err := grokUpstream(authPath)
	if err != nil {
		t.Fatalf("grokUpstream: %v", err)
	}
	models, err := up.ModelDiscovery(context.Background())
	if err != nil {
		t.Fatalf("ModelDiscovery: %v", err)
	}
	found := map[string]bool{}
	for _, m := range models {
		found[m] = true
	}
	for _, want := range []string{"grok-4.5", "grok-4.3"} {
		if !found[want] {
			t.Fatalf("models = %v, want static fallback to contain %q", models, want)
		}
	}
}

// TestGrokNativeForwardSurvivesStrictValidation drives a Claude-shaped
// /v1/messages body (tools whose schemas omit `required`, nest it, or null
// it) through the real grokUpstream into a mock that validates like Grok's
// subscription endpoint: every object schema must carry a required array.
// Unit tests on the sanitizer prove the rewrite; this proves the wiring —
// the sanitizer actually runs on the native forward instead of being dropped
// by a copy site.
func TestGrokNativeForwardSurvivesStrictValidation(t *testing.T) {
	dir := t.TempDir()
	authPath := writeGrokAuth(t, dir, "https://auth.x.ai::client",
		`{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}`)
	var gotBody []byte
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = raw
		if msg := strictGrokValidate(raw); msg != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"invalid-argument","error":"Invalid request content: Schema validation failed: [standard_violation] ` + msg + `"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","content":[]}`))
	}))
	defer upSrv.Close()

	up, err := grokUpstream(authPath)
	if err != nil {
		t.Fatalf("grokUpstream: %v", err)
	}
	base, _ := url.Parse(upSrv.URL)
	up.BaseURL = base
	up.Anthropic.BaseURL = base

	// Claude-shaped: omitted required, nested omission, and explicit null.
	in := `{"model":"grok-4.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"plain","input_schema":{"type":"object","properties":{"p":{"type":"string"}}}},{"name":"nested","input_schema":{"type":"object","properties":{"f":{"type":"object","properties":{"q":{"type":"string"}}}}}},{"name":"nulled","input_schema":{"type":"object","properties":{},"required":null}}]}`
	rec := httptest.NewRecorder()
	proxy.Serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(in)), up)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("forwarded body is not JSON: %v", err)
	}
	for _, raw := range got["tools"].([]any) {
		schema := raw.(map[string]any)["input_schema"].(map[string]any)
		if req, ok := schema["required"].([]any); !ok || len(req) != 0 {
			t.Fatalf("tool schema required = %#v, want []: %s", schema["required"], gotBody)
		}
	}
}

// strictGrokValidate mimics the subscription endpoint's schema strictness:
// object schemas without a required array are rejected. "" means accept.
func strictGrokValidate(raw []byte) string {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "unparseable"
	}
	tools, _ := payload["tools"].([]any)
	for _, tr := range tools {
		tool, _ := tr.(map[string]any)
		schema, _ := tool["input_schema"].(map[string]any)
		if schema == nil {
			continue
		}
		if msg := strictGrokSchema(schema); msg != "" {
			return msg
		}
	}
	return ""
}

func strictGrokSchema(schema map[string]any) string {
	if _, ok := schema["required"]; !ok && isGrokObjectSchema(schema) {
		return "/required: null is not of type \"array\""
	}
	if req, ok := schema["required"]; ok && req == nil {
		return "/required: null is not of type \"array\""
	}
	return ""
}
