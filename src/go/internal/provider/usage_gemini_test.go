package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codeAssistQuotaServer stands in for the Code Assist endpoints. It records
// request counts and the bodies so a test can prove the usage read used the
// already-live access token (no refresh, no writes) and the right client
// metadata.
type codeAssistQuotaServer struct {
	loadCalls  int
	quotaCalls int
	loadBody   map[string]interface{}
	quotaBody  map[string]interface{}
	authorize  string
	project    string
	buckets    []map[string]interface{}
	status     int
}

func (s *codeAssistQuotaServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.authorize = r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "loadCodeAssist"):
			s.loadCalls++
			_ = json.NewDecoder(r.Body).Decode(&s.loadBody)
			if s.status != 0 {
				w.WriteHeader(s.status)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"cloudaicompanionProject": s.project,
			})
		case strings.HasSuffix(r.URL.Path, "retrieveUserQuota"):
			s.quotaCalls++
			_ = json.NewDecoder(r.Body).Decode(&s.quotaBody)
			if s.status != 0 {
				w.WriteHeader(s.status)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"buckets": s.buckets})
		default:
			t.Errorf("unexpected Code Assist path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GEMINI_CODE_ASSIST_UPSTREAM", server.URL)
	return server
}

func writeGeminiCLICredential(t *testing.T, home, accessToken, refreshToken string, expiry time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"access_token":"` + accessToken + `","refresh_token":"` + refreshToken +
		`","token_type":"Bearer","expiry_date":` + formatUnixMilli(expiry) + `}`
	if err := os.WriteFile(filepath.Join(dir, geminiCliCredentialFiles[0]), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiLiveUsageReadsCodeAssistQuota(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	writeGeminiCLICredential(t, home, "cli-access", "cli-refresh", time.Now().Add(time.Hour))

	server := &codeAssistQuotaServer{
		project: "projects/123",
		buckets: []map[string]interface{}{
			{"modelId": "models/gemini-2.5-pro", "remainingFraction": 0.42,
				"resetTime": "2026-09-16T00:00:00Z"},
			{"modelId": "models/gemini-2.5-flash", "remainingFraction": 0.42,
				"resetTime": "2026-09-16T00:00:00Z"},
			{"modelId": "models/claude-sonnet-4", "remainingFraction": 0.9},
		},
	}
	server.start(t)

	usage, err := newGemini(home).LiveUsage(context.Background())
	if err != nil {
		t.Fatalf("LiveUsage: %v", err)
	}
	if usage == nil || len(usage.Windows) != 2 {
		t.Fatalf("windows = %+v, want two grouped windows", usage)
	}
	if server.loadCalls != 1 || server.quotaCalls != 1 {
		t.Fatalf("load=%d quota=%d, want one call each", server.loadCalls, server.quotaCalls)
	}
	if server.authorize != "Bearer cli-access" {
		t.Fatalf("Authorization = %q", server.authorize)
	}
	if server.quotaBody["project"] != "projects/123" {
		t.Fatalf("quota project = %v", server.quotaBody["project"])
	}
	metadata, _ := server.loadBody["metadata"].(map[string]interface{})
	if metadata == nil || metadata["ideType"] != "GEMINI_CLI" {
		t.Fatalf("loadCodeAssist metadata = %v", server.loadBody["metadata"])
	}
	// Buckets that share a fraction and reset instant collapse to a family
	// label; a bucket with a different fraction keeps its own model id.
	if usage.Windows[0].Label != "Gemini (2)" || usage.Windows[0].RemainingPercent != 42 {
		t.Fatalf("grouped window = %+v", usage.Windows[0])
	}
	if usage.Windows[1].Label != "claude-sonnet-4" || usage.Windows[1].RemainingPercent != 90 {
		t.Fatalf("single window = %+v", usage.Windows[1])
	}
	if usage.Windows[0].ResetsAtMs == nil {
		t.Fatal("resetTime must survive as a reset instant")
	}
}

func TestGeminiLiveUsageSkipsExpiredTokenWithoutRefreshing(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	writeGeminiCLICredential(t, home, "cli-access", "cli-refresh", time.Now().Add(-time.Hour))

	server := &codeAssistQuotaServer{project: "projects/123"}
	server.start(t)

	usage, err := newGemini(home).LiveUsage(context.Background())
	if err != nil {
		t.Fatalf("LiveUsage: %v", err)
	}
	if usage != nil {
		t.Fatalf("expired login must not report usage: %+v", usage)
	}
	if server.loadCalls != 0 || server.quotaCalls != 0 {
		t.Fatalf("quota polling must not refresh: load=%d quota=%d", server.loadCalls, server.quotaCalls)
	}
}

func TestGeminiLiveUsageReadsAntigravityState(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	writeAntigravityState(t, home, antigravityTokenSentinel,
		antigravityTokenInfo("anti-access", "Bearer", "1//anti", time.Now().Add(time.Hour)))

	server := &codeAssistQuotaServer{
		project: "projects/9",
		buckets: []map[string]interface{}{
			{"modelId": "models/gemini-3-pro", "remainingFraction": 1},
		},
	}
	server.start(t)

	usage, err := newGemini(home).LiveUsage(context.Background())
	if err != nil {
		t.Fatalf("LiveUsage: %v", err)
	}
	if usage == nil || len(usage.Windows) != 1 || usage.Windows[0].RemainingPercent != 100 {
		t.Fatalf("windows = %+v", usage)
	}
	if server.authorize != "Bearer anti-access" {
		t.Fatalf("Authorization = %q", server.authorize)
	}
	metadata, _ := server.loadBody["metadata"].(map[string]interface{})
	if metadata == nil || metadata["ideType"] != "ANTIGRAVITY" || metadata["platform"] != nil {
		t.Fatalf("loadCodeAssist metadata = %v", server.loadBody["metadata"])
	}
}

func TestGeminiLiveUsageWithoutLiveLoginIsEmpty(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	server := &codeAssistQuotaServer{project: "projects/1"}
	server.start(t)

	usage, err := newGemini(t.TempDir()).LiveUsage(context.Background())
	if err != nil || usage != nil {
		t.Fatalf("usage = %+v err=%v, want nothing", usage, err)
	}
	if server.loadCalls != 0 || server.quotaCalls != 0 {
		t.Fatalf("no live token must not call Code Assist: load=%d quota=%d",
			server.loadCalls, server.quotaCalls)
	}
}

func TestGeminiLiveUsageSurfacesVendorFailureAsNoUsage(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	writeGeminiCLICredential(t, home, "cli-access", "cli-refresh", time.Now().Add(time.Hour))
	server := &codeAssistQuotaServer{project: "projects/1", status: http.StatusForbidden}
	server.start(t)

	usage, err := newGemini(home).LiveUsage(context.Background())
	if err == nil {
		t.Fatal("a vendor rejection must be reported so the snapshot skips the row")
	}
	if usage != nil {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseGeminiQuotaIgnoresMalformedBuckets(t *testing.T) {
	usage, err := parseGeminiQuota([]byte(`{"buckets":[
		{"modelId":"models/gemini-2.5-pro","remainingFraction":1.4},
		{"modelId":"","remainingFraction":0.5},
		{"modelId":"models/gemini-2.5-flash","remainingFraction":"half"},
		{"modelId":"models/gpt-oss-120b","remainingFraction":-0.2}
	]}`))
	if err != nil {
		t.Fatalf("parseGeminiQuota: %v", err)
	}
	if usage == nil || len(usage.Windows) != 2 {
		t.Fatalf("windows = %+v", usage)
	}
	// Clamped to 0..100: 1.4 → 100, -0.2 → 0.
	if usage.Windows[0].RemainingPercent != 100 {
		t.Fatalf("clamp high = %d", usage.Windows[0].RemainingPercent)
	}
	if usage.Windows[1].RemainingPercent != 0 {
		t.Fatalf("clamp low = %d", usage.Windows[1].RemainingPercent)
	}
}

func TestParseGeminiQuotaEmptyIsNil(t *testing.T) {
	usage, err := parseGeminiQuota([]byte(`{"buckets":[]}`))
	if err != nil || usage != nil {
		t.Fatalf("usage = %+v err=%v", usage, err)
	}
}
