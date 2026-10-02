package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/proxy"
)

// quotaUpstream is a stub vendor whose answer the test controls, counting hits
// per account so the chain's choices are observable.
type quotaUpstream struct {
	srv      *httptest.Server
	status   atomic.Int32
	body     string
	patterns []string
	hits     atomic.Int32
}

func newQuotaUpstream(t *testing.T, status int, body string) *quotaUpstream {
	t.Helper()
	up := &quotaUpstream{body: body}
	up.status.Store(int32(status))
	up.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		status := int(up.status.Load())
		if status == 429 {
			w.Header().Set("Retry-After", "3600")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(up.body))
	}))
	t.Cleanup(up.srv.Close)
	return up
}

// chainResolver resolves every source to its own stub.
type chainResolver map[string]*quotaUpstream

func (r chainResolver) ResolveUpstream(_ context.Context, providerID, account string) (*proxy.Upstream, error) {
	up, ok := r[providerID+"/"+account]
	if !ok {
		return nil, context.Canceled
	}
	base, _ := url.Parse(up.srv.URL)
	return &proxy.Upstream{BaseURL: base, QuotaErrorPatterns: up.patterns}, nil
}

// quotaChainServer builds a hub over the given accounts, with the hub-only quota
// guard enabled at a controllable clock.
func quotaChainServer(
	t *testing.T, strategy string, resolver chainResolver, members []Source,
	policy config.QuotaGuardPolicy, now func() time.Time,
) (*Server, string) {
	t.Helper()
	store := newHubStore(t)
	for _, m := range members {
		if strings.HasPrefix(m.Account, "pool:") {
			continue
		}
		seedHubAccounts(t, store, m.ProviderID+"/"+m.Account)
	}
	saved, err := store.UpsertConfigContext(context.Background(), Config{
		Name: "chain", Enabled: true, Strategy: strategy, Sources: members,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := mustNewProxyToken(t)
	if err := store.AttachRouteContext(context.Background(), Route{
		RouteID: DefaultRouteID("chain"), HubName: "chain", ClientID: "claude", Token: token,
	}); err != nil {
		t.Fatal(err)
	}
	guard := newHubQuotas(policy, filepath.Join(t.TempDir(), "quota-guard.json"), now)
	return &Server{
		Config:   saved,
		Store:    NewService(store),
		Resolver: resolver,
		Quotas:   guard,
	}, token
}

// twoAccounts is the source list every chain test starts from.
func twoAccounts() []Source {
	return []Source{
		{ProviderID: "deepseek", Account: "a"},
		{ProviderID: "deepseek", Account: "b"},
	}
}

// permissivePolicy is the guard policy the chain tests use unless they are
// asserting the refusal.
func permissivePolicy() config.QuotaGuardPolicy {
	return config.QuotaGuardPolicy{Enabled: true, CooldownMinutes: 60, ServeWhenAllCooling: true}
}

func ptr[T any](value T) *T { return &value }

// quotaBody is the vendor text that attributes exhaustion to the credential.
const quotaBody = `{"error":{"message":"Resource exhausted: quota exceeded for API key"}}`

func quotaRequest(t *testing.T, srv *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.serveRoute(rec, req)
	return rec
}

// A quota refusal from the account the hub picked must reach the next enabled
// account that can serve the same model, and the refused account must cool down.
func TestHubFailsOverAQuotaRefusal(t *testing.T) {
	upA := newQuotaUpstream(t, 429, quotaBody)
	upB := newQuotaUpstream(t, 200, `{"ok":true}`)
	now := time.Now()
	srv, token := quotaChainServer(t, StrategyFailover, chainResolver{
		"deepseek/a": upA, "deepseek/b": upB,
	}, twoAccounts(), permissivePolicy(), func() time.Time { return now })

	rec := quotaRequest(t, srv, token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("first request = %d %s, want the second account's answer", rec.Code, rec.Body.String())
	}
	if upA.hits.Load() != 1 || upB.hits.Load() != 1 {
		t.Fatalf("hits = %d/%d, want one attempt each", upA.hits.Load(), upB.hits.Load())
	}

	// The refused account is cooling now: the next request goes straight to B.
	rec = quotaRequest(t, srv, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("second request = %d %s", rec.Code, rec.Body.String())
	}
	if upA.hits.Load() != 1 {
		t.Fatalf("cooling account was retried: %d hits", upA.hits.Load())
	}
	if upB.hits.Load() != 2 {
		t.Fatalf("eligible account hits = %d, want 2", upB.hits.Load())
	}
}

// A refusal the vendor does not attribute to the credential is the caller's
// answer, not a reason to spend another account's quota.
func TestHubPassesThroughNonQuotaFailures(t *testing.T) {
	upA := newQuotaUpstream(t, 500, `{"error":{"message":"internal"}}`)
	upB := newQuotaUpstream(t, 200, `{"ok":true}`)
	srv, token := quotaChainServer(t, StrategyFailover, chainResolver{
		"deepseek/a": upA, "deepseek/b": upB,
	}, twoAccounts(), permissivePolicy(), time.Now)

	rec := quotaRequest(t, srv, token)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal") {
		t.Fatalf("response = %d %s, want the vendor's 500 verbatim", rec.Code, rec.Body.String())
	}
	if upB.hits.Load() != 0 {
		t.Fatalf("a non-quota failure still used the other account: %d hits", upB.hits.Load())
	}
}

// When every candidate is cooling, the policy decides: serve the
// soonest-to-recover account and pass its verdict through, exactly once.
func TestHubServesTheSoonestToRecoverWhenAllCooling(t *testing.T) {
	upA := newQuotaUpstream(t, 429, quotaBody)
	upB := newQuotaUpstream(t, 429, quotaBody)
	now := time.Now()
	srv, token := quotaChainServer(t, StrategyFailover, chainResolver{
		"deepseek/a": upA, "deepseek/b": upB,
	}, twoAccounts(), permissivePolicy(), func() time.Time { return now })

	// Cool both accounts, B recovering sooner.
	srv.Quotas.exhaust(SourceKey("deepseek", "a"), ptr(int64(3_600_000)))
	srv.Quotas.exhaust(SourceKey("deepseek", "b"), ptr(int64(60_000)))

	rec := quotaRequest(t, srv, token)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("all-cooling response = %d %s, want the vendor's 429", rec.Code, rec.Body.String())
	}
	if upB.hits.Load() != 1 || upA.hits.Load() != 0 {
		t.Fatalf("hits = a:%d b:%d, want only the soonest-to-recover account", upA.hits.Load(), upB.hits.Load())
	}
}

// With the policy off, a fully cooled chain refuses locally instead of serving.
func TestHubRefusesWhenAllCoolingAndPolicyOff(t *testing.T) {
	upA := newQuotaUpstream(t, 429, quotaBody)
	srv, token := quotaChainServer(t, StrategyFailover, chainResolver{"deepseek/a": upA},
		twoAccounts()[:1], config.QuotaGuardPolicy{Enabled: true, CooldownMinutes: 60}, time.Now)
	srv.Quotas.exhaust(SourceKey("deepseek", "a"), ptr(int64(3_600_000)))

	rec := quotaRequest(t, srv, token)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("all-cooling response = %d %s", rec.Code, rec.Body.String())
	}
	if upA.hits.Load() != 0 {
		t.Fatalf("a cooling account was attempted: %d hits", upA.hits.Load())
	}
}

// Rotation starts at the cursor, but a cooling account is skipped: the mode
// spreads load over the accounts that can actually take it.
func TestHubRotationSkipsCoolingAccounts(t *testing.T) {
	upA := newQuotaUpstream(t, 200, `{"ok":true}`)
	upB := newQuotaUpstream(t, 200, `{"ok":true}`)
	now := time.Now()
	srv, token := quotaChainServer(t, StrategyRoundRobin, chainResolver{
		"deepseek/a": upA, "deepseek/b": upB,
	}, twoAccounts(), permissivePolicy(), func() time.Time { return now })
	srv.Quotas.exhaust(SourceKey("deepseek", "a"), ptr(int64(3_600_000)))

	for i := 0; i < 3; i++ {
		if rec := quotaRequest(t, srv, token); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if upA.hits.Load() != 0 {
		t.Fatalf("cooling account served a request: %d hits", upA.hits.Load())
	}
	if upB.hits.Load() != 3 {
		t.Fatalf("eligible account hits = %d, want 3", upB.hits.Load())
	}
}

// Cooldowns survive a hub restart: they live in the guard's own file.
func TestHubQuotaCooldownsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-guard.json")
	policy := permissivePolicy()
	first := newHubQuotas(policy, path, time.Now)
	first.exhaust(SourceKey("deepseek", "a"), ptr(int64(1_800_000)))

	second := newHubQuotas(policy, path, time.Now)
	if !second.isCooling(SourceKey("deepseek", "a")) {
		t.Fatal("a cooldown did not survive the restart")
	}
	if second.isCooling(SourceKey("deepseek", "b")) {
		t.Fatal("an unrelated account was cooled")
	}
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var state quotaState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Cooling[SourceKey("deepseek", "a")] == 0 {
		t.Fatalf("state = %s", raw)
	}
	if strings.Contains(string(raw), "quotaBody") {
		t.Fatal("state leaked a message")
	}
}

// The guard is hub-only: a disabled policy means no guard at all.
func TestHubQuotaGuardIsOptional(t *testing.T) {
	if guard := NewQuotas(config.QuotaGuardPolicy{Enabled: false}, filepath.Join(t.TempDir(), "x.json")); guard != nil {
		t.Fatal("a disabled policy must not build a guard")
	}
}

// A 429 matching the upstream's QuotaErrorPatterns (e.g. WorkBuddy's frequency limit)
// must trigger failover to the next eligible account.
func TestHubFailoverOnProviderQuotaPatterns(t *testing.T) {
	upA := newQuotaUpstream(t, 429, `{"code":6004,"msg":"usage exceeds frequency limit"}`)
	upA.patterns = []string{"frequency limit", "6004"}
	upB := newQuotaUpstream(t, 200, `{"ok":true,"account":"b"}`)

	srv, token := quotaChainServer(t, StrategyFailover, chainResolver{
		"deepseek/a": upA, "deepseek/b": upB,
	}, twoAccounts(), permissivePolicy(), time.Now)

	rec := quotaRequest(t, srv, token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"account":"b"`) {
		t.Fatalf("response = %d %s, want failover to account b with 200 OK", rec.Code, rec.Body.String())
	}
	if upA.hits.Load() != 1 {
		t.Fatalf("account a hits = %d, want 1", upA.hits.Load())
	}
	if upB.hits.Load() != 1 {
		t.Fatalf("account b hits = %d, want 1", upB.hits.Load())
	}
}
