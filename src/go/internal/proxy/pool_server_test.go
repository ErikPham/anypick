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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/pools"
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
	// headers are applied to every response, so a test can assert that the
	// capture path preserves arbitrary upstream headers (e.g. Retry-After).
	headers map[string]string
	// delayMs is applied before the status is written, so a test can make one
	// member slower than the aggregate ring budget.
	delayMs atomic.Int64
}

func newStubUpstream(t *testing.T, defaultStatus int) *stubUpstream {
	t.Helper()
	s := &stubUpstream{body: `{"ok":true}`}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if d := s.delayMs.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		for k, v := range s.headers {
			w.Header().Set(k, v)
		}
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

// poolTestOption tunes one test pool (e.g. the all-cooling policy) without
// growing the helper's parameter list for every caller.
type poolTestOption func(*upstream.GuardOptions)

// servesWhenAllCooling turns on the policy that keeps a ring serving once every
// member is cooling.
func servesWhenAllCooling() poolTestOption {
	return func(o *upstream.GuardOptions) { o.ServeWhenAllCooling = true }
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
	options ...poolTestOption,
) (*PoolServer, string) {
	t.Helper()
	token := mustNewToken(t)
	server := &PoolServer{
		ProviderID: "deepseek", InstanceID: instanceID, Token: token,
		Members: members, Strategy: strategy,
		Resolver: resolver,
		Guard: func() *upstream.Guard {
			opts := upstream.GuardOptions{
				Enabled:      true,
				CooldownMs:   60_000,
				AccountNames: members,
				StatePath:    statePath,
				ProviderID:   "deepseek",
			}
			for _, option := range options {
				option(&opts)
			}
			return upstream.NewGuard(opts)
		}(),
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
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a"},
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
			server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
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

// TestPoolRingDeadlineStopsWalkingTheRing pins the aggregate pre-commit
// budget: an uncommitted ring must not keep billing member after member
// forever. Without it a pool of N slow members costs N × upstream timeout per
// client request, which is how a small pool becomes an amplifier.
func TestPoolRingDeadlineStopsWalkingTheRing(t *testing.T) {
	upA := newStubUpstream(t, 200)
	upB := newStubUpstream(t, 200)
	upA.status.Store(429)
	upA.body = `{"error":{"message":"Your account has run out of quota"}}`
	// The first member alone burns more than the whole budget.
	upA.delayMs.Store(400)

	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, filepath.Join(t.TempDir(), "quota-guard.json"), "failover")
	server.RingDeadline = 100 * time.Millisecond

	// The budget bounds each attempt too, not just the loop: a member that is
	// still thinking when the budget expires is abandoned, so one slow
	// upstream cannot hold the client for its own full timeout.
	started := time.Now()
	resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	elapsed := time.Since(started)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 once the ring budget is spent", resp.StatusCode)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("elapsed = %s, want the abandoned member cut near the %s budget",
			elapsed, server.RingDeadline)
	}
	if upA.hitCount() != 1 {
		t.Errorf("first member hits = %d, want 1", upA.hitCount())
	}
	if upB.hitCount() != 0 {
		t.Error("the ring must stop before contacting another member")
	}
}

// TestPoolRingDeadlineNeverCutsACommittedStream is the other half of the
// invariant: the budget bounds *selection*, not delivery. A slow upstream that
// already committed its headers must be allowed to finish, or long streams
// would be truncated exactly when they are working.
func TestPoolRingDeadlineNeverCutsACommittedStream(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte(`{"chunk":"one"}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		_, _ = w.Write([]byte(`{"chunk":"two"}`))
	}))
	defer srv.Close()
	defer releaseAll()

	url := mustParseURLHelper(t, srv.URL)
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a"},
		mapResolver{"a": {BaseURL: url, Auth: "upstream-secret-a"}},
		filepath.Join(t.TempDir(), "quota-guard.json"), "failover")
	// Far shorter than the response takes: only the commit may save it.
	server.RingDeadline = 150 * time.Millisecond

	done := make(chan struct{})
	var body string
	var statusCode int
	go func() {
		defer close(done)
		resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
		statusCode = resp.StatusCode
		raw, _ := io.ReadAll(resp.Body)
		body = string(raw)
	}()
	time.Sleep(400 * time.Millisecond) // deadline is long gone
	releaseAll()
	<-done

	if statusCode != http.StatusOK {
		t.Fatalf("status = %d, want the committed 200", statusCode)
	}
	if !strings.Contains(body, `"chunk":"one"`) || !strings.Contains(body, `"chunk":"two"`) {
		t.Errorf("stream was truncated: %q", body)
	}
}

func TestPoolPassesThroughNonQuotaStatusesVerbatim(t *testing.T) {
	upA := newStubUpstream(t, 500)
	upA.body = `{"error":{"message":"internal"}}`
	upB := newStubUpstream(t, 200)
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
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
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a"},
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
			server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
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
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
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
	server, poolToken := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
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
	// Rotation changes which member starts an attempt, never whether a
	// quota-class failure fails over: a rotating ring that stopped at the first
	// exhausted member would be worse than the sticky one it replaces.
	upA := newStubUpstream(t, 402)
	upB := newStubUpstream(t, 200)
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
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

// TestPoolRoundRobinRotatesPerRequest is the rotation contract: successive
// requests start at successive members instead of every request landing on the
// account that last served.
func TestPoolRoundRobinRotatesPerRequest(t *testing.T) {
	upA := newStubUpstream(t, 200)
	upB := newStubUpstream(t, 200)
	upC := newStubUpstream(t, 200)
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b", "c"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
			"c": {BaseURL: upC.url, Auth: "upstream-secret-c"},
		}, filepath.Join(t.TempDir(), "quota-guard.json"), "round-robin")

	for i := 0; i < 3; i++ {
		if resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions"); resp.StatusCode != 200 {
			t.Fatalf("request %d status = %d", i, resp.StatusCode)
		}
	}
	if got := []int{upA.hitCount(), upB.hitCount(), upC.hitCount()}; got[0] != 1 || got[1] != 1 || got[2] != 1 {
		t.Fatalf("three requests hit [a b c] = %v, want one each: rotation is not advancing", got)
	}
}

// TestPoolFailoverStrategyStaysSticky is the other half of the contract: the
// default strategy keeps serving from the account that last answered, so a
// working account is not rotated away from under a client.
func TestPoolFailoverStrategyStaysSticky(t *testing.T) {
	upA := newStubUpstream(t, 200)
	upB := newStubUpstream(t, 200)
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, filepath.Join(t.TempDir(), "quota-guard.json"), "failover")

	for i := 0; i < 3; i++ {
		poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	}
	if upA.hitCount() != 3 || upB.hitCount() != 0 {
		t.Fatalf("failover hits [a b] = [%d %d], want [3 0]: failover must stay sticky",
			upA.hitCount(), upB.hitCount())
	}
}

// TestPoolRoundRobinSkipsCoolingMembers keeps the two policies composable: the
// guard's cooldown filter still applies while the ring rotates.
func TestPoolRoundRobinSkipsCoolingMembers(t *testing.T) {
	upA := newStubUpstream(t, 200)
	upB := newStubUpstream(t, 200)
	upC := newStubUpstream(t, 200)
	statePath := filepath.Join(t.TempDir(), "quota-guard.json")
	future := time.Now().Add(time.Hour).UnixMilli()
	if err := upstream.WriteState(statePath, upstream.State{
		Version: 1,
		Cooling: map[string]int64{"b": future},
		Events:  []upstream.Event{},
	}); err != nil {
		t.Fatal(err)
	}
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b", "c"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
			"c": {BaseURL: upC.url, Auth: "upstream-secret-c"},
		}, statePath, "round-robin")

	for i := 0; i < 4; i++ {
		if resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions"); resp.StatusCode != 200 {
			t.Fatalf("request %d status = %d", i, resp.StatusCode)
		}
	}
	if upB.hitCount() != 0 {
		t.Fatalf("cooling member b was contacted %d times", upB.hitCount())
	}
	if upA.hitCount() != 2 || upC.hitCount() != 2 {
		t.Fatalf("rotation over the eligible ring = [a %d c %d], want [2 2]", upA.hitCount(), upC.hitCount())
	}
}

// TestPoolAllCoolingServesTheVendorsVerdict is the failover-config contract:
// with the policy on, a fully cooled ring attempts the member that recovers
// soonest and hands the caller the vendor's answer instead of refusing with a
// local 429.
func TestPoolAllCoolingServesTheVendorsVerdict(t *testing.T) {
	upA := newStubUpstream(t, 429)
	upB := newStubUpstream(t, 429)
	statePath := filepath.Join(t.TempDir(), "quota-guard.json")
	now := time.Now().UnixMilli()
	if err := upstream.WriteState(statePath, upstream.State{
		Version: 1,
		Cooling: map[string]int64{"a": now + 3_600_000, "b": now + 1_800_000},
		Events:  []upstream.Event{},
	}); err != nil {
		t.Fatal(err)
	}
	server, token := newTestPool(t, mustNewInstanceID(t), []string{"a", "b"},
		mapResolver{
			"a": {BaseURL: upA.url, Auth: "upstream-secret-a"},
			"b": {BaseURL: upB.url, Auth: "upstream-secret-b"},
		}, statePath, "failover", servesWhenAllCooling())

	resp := poolGet(t, server.Endpoint(), token, "/v1/chat/completions")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the upstream's 429", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "cooling down") {
		t.Fatalf("local policy message leaked: %q", raw)
	}
	// One attempt only: the soonest-to-recover member, not the whole ring.
	if upA.hitCount() != 0 {
		t.Fatalf("member a (cooling longest) was attempted %d times", upA.hitCount())
	}
	if upB.hitCount() != 1 {
		t.Fatalf("member b attempts = %d, want 1", upB.hitCount())
	}
}

// TestReverseEgressCaptureMatchesCommit locks the core invariant of the pool
// pipeline unification: the committing single-account reverse path (Serve) and
// the pool ring's capture leaf (reverseWithCapture) are now the SAME
// reverseEgress, differing only by their sink. For one upstream answer they must
// therefore produce byte-identical output — on success the capture commits and
// streams the same bytes; on a non-quota failure the captured headers (incl.
// Retry-After) and body equal what the committing path streams verbatim.
func TestReverseEgressCaptureMatchesCommit(t *testing.T) {
	up := newStubUpstream(t, 200)
	up.body = `{"echo":"payload"}`

	cases := []struct {
		name       string
		status     int
		retryAfter string
	}{
		{"success-200", 200, ""},
		{"non-quota-400-with-retry-after", 400, "30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.retryAfter != "" {
				up.headers = map[string]string{"Retry-After": tc.retryAfter}
			} else {
				up.headers = nil
			}
			up.status.Store(int32(tc.status))

			// Committing path: Serve streams the real response to the client.
			rec := httptest.NewRecorder()
			reqCommit := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			reqCommit.Header.Set("Content-Type", "application/json")
			Serve(rec, reqCommit, &Upstream{BaseURL: up.url, Auth: "upstream-secret"})

			// Capture path: reverseWithCapture holds the response for the ring.
			inner := httptest.NewRecorder()
			cw := &captureWriter{ResponseWriter: inner}
			reqCapture := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			reqCapture.Header.Set("Content-Type", "application/json")
			(&PoolServer{}).reverseWithCapture(cw, reqCapture, up.url, &Upstream{BaseURL: up.url, Auth: "upstream-secret"}, "")

			if tc.status < 400 {
				if !cw.committed {
					t.Fatalf("capture path did not commit on status %d", tc.status)
				}
				if inner.Code != http.StatusOK {
					t.Fatalf("captured status = %d, want 200", inner.Code)
				}
				if inner.Body.String() != rec.Body.String() {
					t.Fatalf("commit body %q != captured body %q", rec.Body.String(), inner.Body.String())
				}
				return
			}
			if cw.committed {
				t.Fatalf("capture path must not commit a %d response", tc.status)
			}
			if cw.status != tc.status {
				t.Fatalf("captured status = %d, want %d", cw.status, tc.status)
			}
			if cw.body.String() != rec.Body.String() {
				t.Fatalf("commit body %q != captured body %q", rec.Body.String(), cw.body.Bytes())
			}
			if got := cw.header.Get("Retry-After"); got != tc.retryAfter {
				t.Fatalf("captured Retry-After = %q, want %q", got, tc.retryAfter)
			}
		})
	}
}

// TestHubPoolRingCarriesStrategyAndMembers closes the loop between the row the
// Hub reads and the ring it serves: the strategy a user picked is the strategy
// the in-process ring walks, and a paused member stays out of it.
func TestHubPoolRingCarriesStrategyAndMembers(t *testing.T) {
	ctx := context.Background()
	database, err := openPoolTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := newPoolTestService(t, database, mapResolver{})

	if err := service.Pools.Set(ctx, pools.Pool{
		Provider: "deepseek", Mode: "multi", Enabled: true, Strategy: "round-robin",
		Members: []pools.Member{{Account: "a", Enabled: true}, {Account: "b", Enabled: false}},
	}); err != nil {
		t.Fatal(err)
	}
	ring, rerr := service.HubPoolRing(ctx, "deepseek")
	err = rerr
	if err != nil {
		t.Fatalf("HubPoolRing: %v", err)
	}
	if ring.Strategy != "round-robin" {
		t.Fatalf("ring strategy = %q, want the configured round-robin", ring.Strategy)
	}
	if len(ring.Members) != 1 || ring.Members[0] != "a" {
		t.Fatalf("ring members = %v, want only the enabled member", ring.Members)
	}

	// A pool that is not multi+enabled is refused instead of silently served
	// as a one-account ring.
	if err := service.Pools.Set(ctx, pools.Pool{
		Provider: "deepseek", Mode: "multi", Strategy: "round-robin",
		Members: []pools.Member{{Account: "a", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HubPoolRing(ctx, "deepseek"); err == nil {
		t.Fatal("disabled pool still produced a ring")
	}
}
