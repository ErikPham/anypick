package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A compromised or broken OAuth endpoint must not be able to smuggle secrets
// (or attacker-controlled payloads) through our errors into HTTP responses or
// logs. Both refresh paths bound the body and map failures to a status-only
// message. The canary stands in for a refresh token echoed by the endpoint.
func TestGeminiOAuthRefreshErrorCarriesNoVendorBody(t *testing.T) {
	canary := "canary-gemini-refresh-must-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"` + canary + `"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GEMINI_TOKEN_ENDPOINT", srv.URL)

	dir := t.TempDir()
	expired := time.Now().Add(-time.Hour).UnixMilli()
	creds, _ := json.Marshal(map[string]any{
		"access_token":  "expired",
		"refresh_token": "test-refresh-token",
		"token_type":    "Bearer",
		"expiry_date":   expired,
	})
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), creds, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := geminiEnsureAccessToken(dir, geminiCliOAuthSource)
	if err == nil {
		t.Fatal("expired credential against a 400 endpoint must fail")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("vendor body leaked into error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should name the status, got %q", err.Error())
	}
}

func TestGrokOAuthRefreshErrorCarriesNoVendorBody(t *testing.T) {
	canary := "canary-grok-refresh-must-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","refresh_token":"` + canary + `"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GROK_TOKEN_ENDPOINT", srv.URL)

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	expired := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	entry, _ := json.Marshal(map[string]string{
		"key":            "expired-access",
		"refresh_token":  "test-refresh-token",
		"expires_at":     expired,
		"oidc_issuer":    grokOIDCIssuer,
		"oidc_client_id": "test-client",
		"auth_mode":      "oidc",
	})
	top, _ := json.Marshal(map[string]json.RawMessage{
		grokOIDCIssuer + "::test-client": entry,
	})
	if err := os.WriteFile(authPath, top, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := grokEnsureAccessToken(authPath)
	if err == nil {
		t.Fatal("expired session against a 401 endpoint must fail")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("vendor body leaked into error: %q", err.Error())
	}
}

func TestOAuthEndpointOverrideRejectsNonLoopback(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"public https", "https://oauth2.example.com/token"},
		{"public http", "http://192.0.2.1/token"},
		{"non-url", "not a url at all %%"},
		{"file scheme", "file:///tmp/token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GEMINI_TOKEN_ENDPOINT", tc.url)
			if _, err := oauthTokenEndpoint("GEMINI_TOKEN_ENDPOINT", "https://def/token"); err == nil {
				t.Fatalf("non-loopback override %q must fail closed", tc.url)
			} else if strings.Contains(err.Error(), "tok") && strings.Contains(tc.url, "tok") {
				t.Fatalf("error must not echo the URL payload: %q", err.Error())
			}
			if _, err := oauthTokenEndpoint("GEMINI_TOKEN_ENDPOINT", geminiTokenEndpointDefault); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	for _, url := range []string{"http://127.0.0.1:9/token", "https://localhost/token", "http://[::1]/t"} {
		t.Setenv("GEMINI_TOKEN_ENDPOINT", url)
		if got, err := oauthTokenEndpoint("GEMINI_TOKEN_ENDPOINT", "https://def/token"); err != nil || got != url {
			t.Fatalf("loopback override %q must be honored, got %q %v", url, got, err)
		}
	}
}
