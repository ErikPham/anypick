// Route-lifecycle and pool-source failover regressions (NATIVE-M1): a live
// hub must grant, rotate, and revoke per-route tokens, and a pool-backed
// source must fail over on quota-class failures only — never on other
// statuses, which pass through verbatim exactly once.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/upstream"
)

// stubUpstream is one fake vendor endpoint recording every hit.
type stubUpstream struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	hits       int
	status     int
	body       string
	lastAuth   string
	lastAPIKey string
}

func newStubUpstream(t *testing.T, status int, body string) *stubUpstream {
	t.Helper()
	st := &stubUpstream{t: t, status: status, body: body}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = raw
		st.mu.Lock()
		st.hits++
		st.lastAuth = r.Header.Get("Authorization")
		st.lastAPIKey = r.Header.Get("x-api-key")
		status, body := st.status, st.body
		st.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	st.srv = httptest.NewServer(mux)
	t.Cleanup(st.srv.Close)
	return st
}

func (s *stubUpstream) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *stubUpstream) url() *url.URL {
	s.t.Helper()
	u, err := url.Parse(s.srv.URL)
	if err != nil {
		s.t.Fatal(err)
	}
	return u
}

// mapResolver resolves fixed provider/account pairs to stub upstreams.
type mapResolver struct {
	targets map[string]*url.URL
	// creds are per-account upstream credentials; each must be presented to
	// its own stub verbatim and a hub route token must never appear instead.
	creds map[string]string
}

func (m mapResolver) ResolveUpstream(_ context.Context, providerID, account string) (*proxy.Upstream, error) {
	up, ok := m.targets[providerID+"/"+account]
	if !ok {
		return nil, nil
	}
	return &proxy.Upstream{BaseURL: up, Auth: m.creds[providerID+"/"+account]}, nil
}

// requireMemberCredential asserts the egress credential boundary: the stub
// saw exactly its own account's upstream credential and never the hub route
// token (Authorization or x-api-key).
func requireMemberCredential(t *testing.T, s *stubUpstream, cred, routeToken string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if want := "Bearer " + cred; s.lastAuth != want {
		t.Fatalf("upstream Authorization = %q, want member credential %q", s.lastAuth, want)
	}
	if s.lastAPIKey != "" {
		t.Fatalf("upstream x-api-key = %q, want empty for Bearer-scheme upstreams", s.lastAPIKey)
	}
	for _, got := range []string{s.lastAuth, "Bearer " + s.lastAPIKey} {
		if got == "Bearer "+routeToken || got == routeToken || (routeToken != "" && strings.Contains(got, routeToken)) {
			t.Fatalf("hub route token leaked to upstream: %q", got)
		}
	}
}

func mustRouteToken(t *testing.T) string {
	t.Helper()
	token, err := NewRouteToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func attachLifecycle(t *testing.T, st *Store, r Route) {
	t.Helper()
	if err := st.AttachRoute(r); err != nil {
		t.Fatal(err)
	}
}

// postModel sends one credentialed completion request to the live hub.
func postModel(t *testing.T, endpoint, token, model string) (*http.Response, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"model": model, "messages": "[]"})
	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/chat/completions",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

func TestRouteAttachGrantRotateRevoke(t *testing.T) {
	st := newHubStore(t)

	glm := newStubUpstream(t, http.StatusOK, `{"up":"glm"}`)
	ds := newStubUpstream(t, http.StatusOK, `{"up":"ds"}`)
	resolver := mapResolver{
		targets: map[string]*url.URL{
			"glm/cn":      glm.url(),
			"deepseek/ws": ds.url(),
		},
		creds: map[string]string{
			"glm/cn":      "cred-glm-cn",
			"deepseek/ws": "cred-deepseek-ws",
		},
	}

	cfg, err := st.UpsertConfig(Config{
		Name: "lifecycle", Enabled: true,
		Sources: []Source{
			{ProviderID: "glm", Account: "cn"},
			{ProviderID: "deepseek", Account: "ws"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Config: cfg, Store: st, Resolver: resolver}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	const routeID = "lifecycle/route-claude"
	token1 := mustRouteToken(t)
	attachLifecycle(t, st, Route{
		RouteID: routeID, HubName: "lifecycle", ClientID: "claude",
		Token:  token1,
		Models: []ModelRoute{{Model: "ds-chat", SourceIndex: 1}},
	})

	// Grant: the pinned model routes to source 1's stub; unpinned models use
	// the default (source 0).
	resp, body := postModel(t, srv.Endpoint(), token1, "ds-chat")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"up":"ds"`) {
		t.Fatalf("pinned model = %d %s, want 200 {\"up\":\"ds\"}", resp.StatusCode, body)
	}
	requireMemberCredential(t, ds, "cred-deepseek-ws", token1)
	resp, body = postModel(t, srv.Endpoint(), token1, "glm-4.7")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"up":"glm"`) {
		t.Fatalf("default model = %d %s, want 200 {\"up\":\"glm\"}", resp.StatusCode, body)
	}
	requireMemberCredential(t, glm, "cred-glm-cn", token1)

	// Rotate (re-attach same route id, fresh secret): old token 401, new 200.
	token2 := mustRouteToken(t)
	if token2 == token1 {
		t.Fatal("rotated token reused the old secret")
	}
	attachLifecycle(t, st, Route{
		RouteID: routeID, HubName: "lifecycle", ClientID: "claude",
		Token:  token2,
		Models: []ModelRoute{{Model: "ds-chat", SourceIndex: 1}},
	})
	if resp, _ := postModel(t, srv.Endpoint(), token1, "ds-chat"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old token after rotate = %d, want 401", resp.StatusCode)
	}
	if resp, body := postModel(t, srv.Endpoint(), token2, "ds-chat"); resp.StatusCode != http.StatusOK ||
		!strings.Contains(body, `"up":"ds"`) {
		t.Fatalf("new token after rotate = %d %s, want 200", resp.StatusCode, body)
	}
	requireMemberCredential(t, ds, "cred-deepseek-ws", token2)

	// Revoke: detach kills the granted credential immediately.
	if err := st.DetachRouteContext(context.Background(), routeID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := postModel(t, srv.Endpoint(), token2, "ds-chat"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token after detach = %d, want 401", resp.StatusCode)
	}
}

type staticRing struct{ srv *proxy.PoolServer }

func (s staticRing) ResolvePoolRing(context.Context, string) (*proxy.PoolServer, error) {
	return s.srv, nil
}

// A pool-backed source (`pool:<provider>` account value) serves through the
// pool credential ring: quota-class failures advance to the next member with
// guard cooldown; other statuses pass through verbatim exactly once.
func TestHubPoolSourceFailsOverQuotaClassOnly(t *testing.T) {
	st := newHubStore(t)

	memberA := newStubUpstream(t, http.StatusUnauthorized,
		`{"error":{"type":"authentication_error","message":"quota out"}}`)
	memberB := newStubUpstream(t, http.StatusOK, `{"up":"b"}`)
	resolver := mapResolver{
		targets: map[string]*url.URL{
			"deepseek/a": memberA.url(),
			"deepseek/b": memberB.url(),
		},
		creds: map[string]string{
			"deepseek/a": "cred-pool-a",
			"deepseek/b": "cred-pool-b",
		},
	}
	guard := upstream.NewGuard(upstream.GuardOptions{
		Enabled:      true,
		AccountNames: []string{"a", "b"},
		StatePath:    filepath.Join(t.TempDir(), "guard.json"),
		ProviderID:   "deepseek",
	})
	ring := &proxy.PoolServer{
		ProviderID: "deepseek", Members: []string{"a", "b"},
		Resolver: resolver, Guard: guard,
	}

	cfg, err := st.UpsertConfig(Config{
		Name: "poolhub", Enabled: true,
		Sources: []Source{{ProviderID: "deepseek", Account: "pool:deepseek"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Config: cfg, Store: st, Resolver: resolver, PoolRings: staticRing{ring}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	token := mustRouteToken(t)
	attachLifecycle(t, st, Route{
		RouteID: DefaultRouteID("poolhub"), HubName: "poolhub", ClientID: "cli", Token: token,
	})

	// Member a's 401 is quota-class → fail over to b, which answers 200.
	resp, body := postModel(t, srv.Endpoint(), token, "any-model")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"up":"b"`) {
		t.Fatalf("pool failover = %d %s, want 200 from member b", resp.StatusCode, body)
	}
	if got := memberA.hitCount(); got != 1 {
		t.Fatalf("member a hits = %d, want exactly 1", got)
	}
	requireMemberCredential(t, memberB, "cred-pool-b", token)
	requireMemberCredential(t, memberA, "cred-pool-a", token)

	// Guard cooled a down: subsequent requests skip straight to b.
	if _, body := postModel(t, srv.Endpoint(), token, "any-model"); !strings.Contains(body, `"up":"b"`) {
		t.Fatalf("post-cooldown request did not come from b: %s", body)
	}
	if got := memberA.hitCount(); got != 1 {
		t.Fatalf("member a hits after cooldown = %d, want still 1", got)
	}
}

// Non-quota failures never advance the ring: the head member's status passes
// through verbatim exactly once and later members are untouched.
func TestHubPoolSourcePassesNonQuotaThroughOnce(t *testing.T) {
	st := newHubStore(t)

	memberA := newStubUpstream(t, http.StatusInternalServerError,
		`{"error":{"type":"server_error","message":"boom"}}`)
	memberB := newStubUpstream(t, http.StatusOK, `{"up":"b"}`)
	resolver := mapResolver{targets: map[string]*url.URL{
		"deepseek/a": memberA.url(),
		"deepseek/b": memberB.url(),
	}}
	ring := &proxy.PoolServer{
		ProviderID: "deepseek", Members: []string{"a", "b"},
		Resolver: resolver,
		Guard: upstream.NewGuard(upstream.GuardOptions{
			Enabled:      true,
			AccountNames: []string{"a", "b"},
			StatePath:    filepath.Join(t.TempDir(), "guard.json"),
			ProviderID:   "deepseek",
		}),
	}
	cfg, err := st.UpsertConfig(Config{
		Name: "poolhub500", Enabled: true,
		Sources: []Source{{ProviderID: "deepseek", Account: "pool:deepseek"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Config: cfg, Store: st, Resolver: resolver, PoolRings: staticRing{ring}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	token := mustRouteToken(t)
	attachLifecycle(t, st, Route{
		RouteID: DefaultRouteID("poolhub500"), HubName: "poolhub500", ClientID: "cli", Token: token,
	})

	resp, body := postModel(t, srv.Endpoint(), token, "m")
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "boom") ||
		strings.Contains(body, `"up":"b"`) {
		t.Fatalf("non-quota status = %d %s, want verbatim 500 boom", resp.StatusCode, body)
	}
	if got := memberA.hitCount(); got != 1 {
		t.Fatalf("member a hits = %d, want exactly 1", got)
	}
	if got := memberB.hitCount(); got != 0 {
		t.Fatalf("member b served a non-quota failure: %d hits, want 0", got)
	}
}

// A disabled or empty pool behind a pool-backed source fails closed with a
// clear upstream error instead of silently serving nothing.
func TestHubPoolSourceFailsClosedWhenPoolDisabled(t *testing.T) {
	st := newHubStore(t)
	cfg, err := st.UpsertConfig(Config{
		Name: "deadpool", Enabled: true,
		Sources: []Source{{ProviderID: "deepseek", Account: "pool:deepseek"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rings := failingRings{}
	srv := &Server{Config: cfg, Store: st, PoolRings: rings}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	token := mustRouteToken(t)
	attachLifecycle(t, st, Route{
		RouteID: DefaultRouteID("deadpool"), HubName: "deadpool", ClientID: "cli", Token: token,
	})
	resp, body := postModel(t, srv.Endpoint(), token, "m")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "multi-mode is off") {
		t.Fatalf("disabled pool = %d %s, want 502 pool-disabled error", resp.StatusCode, body)
	}
}

// failingRings stands in for an unwired/disabled pool resolution.
type failingRings struct{}

func (failingRings) ResolvePoolRing(context.Context, string) (*proxy.PoolServer, error) {
	return nil, errors.New("Pool multi-mode is off for deepseek")
}
