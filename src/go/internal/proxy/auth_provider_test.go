package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestUpstreamAuthProviderOverridesStatic verifies the proxy calls an
// AuthProvider per request and injects ExtraHeaders, and that a nil provider
// falls back to the static Auth (backward compatible).
func TestUpstreamAuthProviderOverridesStatic(t *testing.T) {
	var gotAuth, gotVersion string
	var providerCalls int
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("x-grok-client-version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upSrv.Close()

	base, err := url.Parse(upSrv.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}

	// With an AuthProvider + ExtraHeaders.
	up := &Upstream{
		BaseURL:      base,
		Auth:         "static-ignored",
		AuthProvider: func(ctx context.Context) (string, error) { providerCalls++; return "dynamic-token", nil },
		ExtraHeaders: []ExtraHeader{{Name: "x-grok-client-version", Value: "0.2.101"}},
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	Serve(rec, req, up)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if gotAuth != "Bearer dynamic-token" {
		t.Errorf("auth = %q, want Bearer dynamic-token", gotAuth)
	}
	if gotVersion != "0.2.101" {
		t.Errorf("x-grok-client-version = %q, want 0.2.101", gotVersion)
	}
	if providerCalls != 1 {
		t.Errorf("AuthProvider called %d times, want 1", providerCalls)
	}

	// Without an AuthProvider: static Auth wins, no extra header.
	gotAuth, gotVersion = "", ""
	up.AuthProvider = nil
	up.ExtraHeaders = nil
	rec2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	Serve(rec2, req2, up)
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	if gotAuth != "Bearer static-ignored" {
		t.Errorf("static auth = %q, want Bearer static-ignored", gotAuth)
	}
	if gotVersion != "" {
		t.Errorf("x-grok-client-version = %q, want empty", gotVersion)
	}
	if providerCalls != 1 {
		t.Errorf("AuthProvider should not be called when nil, got %d", providerCalls)
	}
}

func TestApplyExtraHeadersDropRemovesInbound(t *testing.T) {
	var gotIntent string
	var sawIntent bool
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawIntent = r.Header["X-Agent-Intent"]
		gotIntent = r.Header.Get("X-Agent-Intent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upSrv.Close)
	base, err := url.Parse(upSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	up := &Upstream{
		BaseURL: base,
		Auth:    "tok",
		DynamicHeaders: func(context.Context) ([]ExtraHeader, error) {
			return []ExtraHeader{{Name: "X-Agent-Intent", Drop: true}}, nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("X-Agent-Intent", "craft")
	Serve(httptest.NewRecorder(), req, up)
	if sawIntent || gotIntent != "" {
		t.Fatalf("X-Agent-Intent leaked to upstream: present=%v value=%q", sawIntent, gotIntent)
	}
}

func TestUpstreamAuthProviderErrorFailsClosed(t *testing.T) {
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream must not be reached when refresh fails (auth=%q)", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer upSrv.Close()
	base, err := url.Parse(upSrv.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	up := &Upstream{
		BaseURL:      base,
		Auth:         "static-stale",
		AuthProvider: func(ctx context.Context) (string, error) { return "", context.DeadlineExceeded },
	}
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"generic", mustReq(t, http.MethodGet, "http://127.0.0.1/v1/models")},
		{"translated", mustReqWithBody(t, "/v1/messages", `{"model":"m","max_tokens":4,"messages":[{"role":"user","content":"hi"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Serve(rec, tc.req, up)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("code = %d, want 502 fail-closed", rec.Code)
			}
			if got := rec.Body.String(); !containsAll(got, "proxy_error", "credential refresh failed") {
				t.Fatalf("body = %s, want proxy_error/credential refresh failed", got)
			}
			if containsAll(rec.Body.String(), "static-stale") {
				t.Fatalf("stale credential leaked into error body")
			}
		})
	}
}

func mustReq(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func mustReqWithBody(t *testing.T, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1"+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
