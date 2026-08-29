// PoolServer regressions: bearer auth on every credentialed route,
// unauthenticated /health ownership echo, quota-class-only failover over the
// member ring, verbatim pass-through of everything else, Quota Guard state
// persistence (secret-free), and the intentional round-robin→failover parity.
package proxy

import (
	"context"
	"encoding/json"
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

	"github.com/outof0/anypick/go/internal/upstream"
)

// mapResolver points each member at its own stub upstream.
type mapResolver map[string]*Upstream

func (m mapResolver) ResolveUpstream(_ context.Context, _, account string) (*Upstream, error) {
	if up, ok := m[account]; ok {
		return up, nil
	}
	return nil, fmt.Errorf("no upstream for %q", account)
}

type stubUpstream struct {
	srv    *httptest.Server
	url    *url.URL
	status atomic.Int32 // status returned per request; 200 default
	hits   atomic.Int32
	body   string
}

func newStubUpstream(t *testing.T, defaultStatus int) *stubUpstream {
	t.Helper()
	s := &stubUpstream{body: `{"ok":true}`}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		code := int(s.status.Load())
		if code == 0 {
			code = defaultStatus
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(s.body))
	}))
	s.url = mustParseURLHelper(t, s.srv.URL)
	t.Cleanup(s.srv.Close)
	return s
}

func mustParseURLHelper(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// newTestPool builds a PoolServer over named stub members with the guard
// enabled and a fresh state file.
func newTestPool(
	t *testing.T,
	instanceID string,
	members []string,
	resolver UpstreamResolver,
	statePath string,
	strategy string,
) (*PoolServer, string) {
	t.Helper()
	token := MustNewToken()
	server := &PoolServer{
		ProviderID: "deepseek", InstanceID: instanceID, Token: token,
		Members: members, Strategy: strategy,
		Resolver: resolver,
		Guard: upstream.NewGuard(upstream.GuardOptions{
			Enabled:      true,
			CooldownMs:   60_000,
			AccountNames: members,
			StatePath:    statePath,
			ProviderID:   "deepseek",
		}),
	}
	if err := server.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	if err := server.WaitReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, token
}

func poolGet(t *testing.T, endpoint, token, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, endpoint+path, strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestPoolServerAuthAndHealthEcho(t *testing.T) {
	upA := newStubUpstream(t, 200)
	server, token := newTestPool(t, MustNewInstanceID(), []string{"a"},
		mapResolver{"a": {BaseURL: upA.url, Auth: "upstream-secret-a"}}, filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	// Missing credentials → 401 without reaching any upstream.
	if resp := poolGet(t, server.Endpoint(), "", "/v1/chat/completions"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bearer = %d, want 401", resp.StatusCode)
	}
	// Wrong credentials → 401.
	if resp := poolGet(t, server.Endpoint(), "wrong-token", "/v1/chat/completions"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d, want 401", resp.StatusCode)
	}

	// /health is unauthenticated and echoes instance + pid + provider so stop
	// paths can prove ownership without numeric PIDs (ADR 0003 §3).
	resp, err := http.Get(server.Endpoint() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceId"`
		PID        int    `json:"pid"`
		Provider   string `json:"provider"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if !health.OK || health.InstanceID != server.InstanceID || health.PID <= 0 || health.Provider != "deepseek" {
		t.Fatalf("health echo mismatch: %+v", health)
	}

	// Correct credentials reach the member upstream with injected credentials.
	if resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions"); resp.StatusCode != 200 {
		t.Fatalf("authenticated request = %d, want 200", resp.StatusCode)
	}
}

func TestPoolFailoverOnQuotaClassFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"401-auth", 401, `{"error":{"message":"bad key"}}`},
		{"402-billing", 402, `{"error":{"message":"payment required"}}`},
		{"429-credential-quota", 429, `{"error":{"message":"Your account has run out of quota"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upA := newStubUpstream(t, 200)
			upB := newStubUpstream(t, 200)
			upA.status.Store(int32(tc.status))
			upA.body = tc.body
			statePath := filepath.Join(t.TempDir(), "quota-guard.json")
			server, token := newTestPool(t, MustNewInstanceID(), []string{"a", "b"},
				mapResolver{
					"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
					"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
				}, statePath, "failover")

			resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
			if resp.StatusCode != 200 {
				t.Fatalf("failover did not reach healthy member: %d", resp.StatusCode)
			}
			if upB.hitCount() == 0 {
				t.Fatal("successor member was never contacted")
			}

			// The exhaustion was persisted: a cools down, b is promoted.
			state, err := upstream.ReadState(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if state.ActiveAccount != "b" {
				t.Errorf("activeAccount = %q, want promoted successor %q", state.ActiveAccount, "b")
			}
			if len(state.Cooling) != 1 || state.Cooling["a"] <= time.Now().UnixMilli() {
				t.Errorf("cooling = %v, want exhausted predecessor only", state.Cooling)
			}
			if len(state.Events) != 1 || state.Events[0].From != "a" || state.Events[0].To != "b" {
				t.Errorf("events = %+v, want one a→b audit event", state.Events)
			}
		})
	}
}

func (s *stubUpstream) hitCount() int { return int(s.hits.Load()) }

func TestPoolPassesThroughNonQuotaStatusesVerbatim(t *testing.T) {
	upA := newStubUpstream(t, 500)
	upA.body = `{"error":{"message":"internal"}}`
	upB := newStubUpstream(t, 200)
	server, token := newTestPool(t, MustNewInstanceID(), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	// A plain server error is NOT quota-class: it reaches the caller verbatim
	// and never burns the successor.
	resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want verbatim 500", resp.StatusCode)
	}
	if upB.hitCount() != 0 {
		t.Error("successor must not be contacted for non-quota failures")
	}
	// Exact bytes, exactly once — capture buffering plus the ReverseProxy body
	// replay used to double every passthrough body.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != upA.body {
		t.Fatalf("passthrough body = %q, want exactly %q", got, upA.body)
	}
}

// The LAST member's quota failure has no successor to fail over to, so the
// captured response is flushed verbatim — also exactly once.
func TestPoolLastMemberQuotaFailureReplaysBodyExactlyOnce(t *testing.T) {
	upA := newStubUpstream(t, 429)
	upA.body = `{"error":{"type":"rate_limit_error","message":"quota exceeded"}}`
	server, token := newTestPool(t, MustNewInstanceID(), []string{"a"},
		mapResolver{"a": {BaseURL: upA.url, Auth: "upstream-secret-a"}},
		filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want verbatim 429", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != upA.body {
		t.Fatalf("last-member quota body = %q, want exactly %q (once)", got, upA.body)
	}
}

// Provider-declared quotaErrorPatterns must trigger failover like TS
// backend.ts isExhausted — e.g. zai codes 1113/1302 riding a non-quota status.
func TestPoolFailoverOnProviderQuotaErrorPattern(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		wantHitB bool
		wantBody string
	}{
		{name: "pattern-matches-failover", patterns: []string{"1113", "1302"}, wantHitB: true},
		{name: "no-patterns-passthrough", patterns: nil, wantHitB: false,
			wantBody: `{"error":{"code":"1113","message":"free tier exhausted"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upA := newStubUpstream(t, 500)
			upA.body = `{"error":{"code":"1113","message":"free tier exhausted"}}`
			upB := newStubUpstream(t, 200)
			server, token := newTestPool(t, MustNewInstanceID(), []string{"a", "b"},
				mapResolver{
					"a": {BaseURL: upA.url, Auth: "upstream-secret-a",
						QuotaErrorPatterns: tc.patterns},
					"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
				}, filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

			resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
			if tc.wantHitB {
				if resp.StatusCode != 200 {
					t.Fatalf("configured pattern must trigger failover, got %d", resp.StatusCode)
				}
				if upB.hitCount() == 0 {
					t.Error("successor never contacted despite provider quota pattern")
				}
			} else {
				if resp.StatusCode != 500 {
					t.Fatalf("without patterns the failure passes through, got %d", resp.StatusCode)
				}
				raw, _ := io.ReadAll(resp.Body)
				if string(raw) != tc.wantBody {
					t.Errorf("body = %q, want verbatim %q", raw, tc.wantBody)
				}
				if upB.hitCount() != 0 {
					t.Error("successor must not be contacted without configured patterns")
				}
			}
		})
	}
}

func TestPoolAllCoolingReturns429(t *testing.T) {
	upA := newStubUpstream(t, 200)
	upB := newStubUpstream(t, 200)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "quota-guard.json")
	future := time.Now().Add(time.Hour).UnixMilli()
	if err := upstream.WriteState(statePath, upstream.State{
		Version: 1,
		Cooling: map[string]int64{"a": future, "b": future},
		Events:  []upstream.Event{},
	}); err != nil {
		t.Fatal(err)
	}
	server, token := newTestPool(t, MustNewInstanceID(), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, statePath, "failover")

	resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("all-cooling status = %d, want 429", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "cooling down") {
		t.Errorf("body %q missing TS parity message", raw)
	}
}

func TestPoolGuardFileIsSecretFree(t *testing.T) {
	upA := newStubUpstream(t, 401)
	upA.body = `{"error":true}`
	upB := newStubUpstream(t, 200)
	statePath := filepath.Join(t.TempDir(), "quota-guard.json")
	server, poolToken := newTestPool(t, MustNewInstanceID(), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, statePath, "failover")

	if resp := poolGet(t, server.Endpoint(), poolToken, "/v1/chat/completions"); resp.StatusCode != 200 {
		t.Fatalf("failover expected 200, got %d", resp.StatusCode)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), poolToken) {
		t.Error("POOL TOKEN LEAKED INTO GUARD FILE")
	}
	for _, secret := range []string{"upstream-secret-a", "upstream-secret-b"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("UPSTREAM SECRET %q LEAKED INTO GUARD FILE", secret)
		}
	}
}

func TestPoolRoundRobinStrategyStillFailsOver(t *testing.T) {
	// Documented intentional parity: the persisted strategy is recorded but
	// rotation is NOT implemented — like TypeScript, round-robin pools behave
	// as ordered-failover with active promotion.
	upA := newStubUpstream(t, 402)
	upB := newStubUpstream(t, 200)
	server, token := newTestPool(t, MustNewInstanceID(), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, filepath.Join(t.TempDir(), "quota-guard.json"), "round-robin")

	resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	if resp.StatusCode != 200 {
		t.Fatalf("round-robin pool must still fail over, got %d", resp.StatusCode)
	}
	if upB.hitCount() == 0 {
		t.Error("successor never contacted under round-robin strategy")
	}
}
