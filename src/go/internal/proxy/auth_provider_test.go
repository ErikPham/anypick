package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
