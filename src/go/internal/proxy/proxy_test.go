package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// ---- primitive security helpers ----

func TestAssertLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost", "[::1]", "LOCALHOST", " 127.0.0.1 "} {
		if err := AssertLoopbackHost(host); err != nil {
			t.Errorf("AssertLoopbackHost(%q) refused: %v", host, err)
		}
	}
	for _, host := range []string{"0.0.0.0", "192.168.1.10", "example.com", ""} {
		if err := AssertLoopbackHost(host); err == nil {
			t.Errorf("AssertLoopbackHost(%q) accepted — must refuse non-loopback", host)
		}
	}
}

func TestSafeEqualFailsClosed(t *testing.T) {
	if !SafeEqual("secret", "secret") {
		t.Error("equal secrets must match")
	}
	if SafeEqual("", "") || SafeEqual("", "x") {
		t.Error("empty expected value must fail closed")
	}
	if SafeEqual("secret", "secrets") || SafeEqual("secret", "SECRET") {
		t.Error("different secrets must not match")
	}
}

func TestNewTokenShape(t *testing.T) {
	a, b := MustNewToken(), MustNewToken()
	if len(a) != 64 || len(b) != 64 {
		t.Fatalf("token length = %d/%d, want 64 hex chars", len(a), len(b))
	}
	if a == b {
		t.Fatal("tokens must be unique per call")
	}
}

func TestNewInstanceIDIsUUIDv4(t *testing.T) {
	id := MustNewInstanceID()
	if len(id) != 36 ||
		id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' ||
		id[14] != '4' {
		t.Fatalf("instance id %q is not uuidv4-shaped", id)
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer abc123")
	if got := bearerToken(r); got != "abc123" {
		t.Errorf("bearer = %q", got)
	}
	r2, _ := http.NewRequest("POST", "/", nil)
	r2.Header.Set("x-api-key", "key456")
	if got := bearerToken(r2); got != "key456" {
		t.Errorf("x-api-key = %q", got)
	}
}

func TestRequireProxyAuthRejects(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/v1/x", nil)
	if requireProxyAuth(rec, req, "tok") {
		t.Fatal("missing credentials must be rejected")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil ||
		payload.Error.Type != "authentication_error" {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// Claude Code can send a claude.ai OAuth token as Authorization: Bearer
// *and* the proxy secret as x-api-key. The Bearer must not mask the key.
func TestRequireProxyAuthAcceptsMatchingXAPIKeyWhenBearerDiffers(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oauth-not-the-proxy")
	req.Header.Set("x-api-key", "tok")
	if !requireProxyAuth(rec, req, "tok") {
		t.Fatal("matching x-api-key must be accepted even when Bearer differs")
	}
}

func TestRequireProxyAuthRejectsWhenNeitherHeaderMatches(t *testing.T) {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer nope")
	req.Header.Set("x-api-key", "also-nope")
	if requireProxyAuth(rec, req, "tok") {
		t.Fatal("mismatched dual headers must be rejected")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// ---- Server behaviour ----

type upstreamProbe struct {
	gotAuth    string
	gotAPIKey  string
	gotPath    string
	gotPayload string
	srv        *httptest.Server
	url        *url.URL
}

func newUpstreamProbe(t testing.TB) *upstreamProbe {
	t.Helper()
	p := &upstreamProbe{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.gotAuth = r.Header.Get("Authorization")
		p.gotAPIKey = r.Header.Get("X-Api-Key")
		p.gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		p.gotPayload = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(p.srv.Close)
	u, err := url.Parse(p.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.url = u
	return p
}

func newTestServer(t testing.TB, upstream *url.URL) *Server {
	t.Helper()
	s := &Server{
		ProviderID:   "codex",
		AccountName:  "work",
		InstanceID:   MustNewInstanceID(),
		Token:        MustNewToken(),
		Upstream:     upstream,
		UpstreamAuth: "upstream-secret",
	}
	if err := s.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestServerHealthUnauthenticatedEchoesOwnership(t *testing.T) {
	up := newUpstreamProbe(t)
	s := newTestServer(t, up.url)

	resp, err := http.Get(s.Endpoint + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var payload struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceId"`
		PID        int    `json:"pid"`
		Provider   string `json:"provider"`
		Account    string `json:"account"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("health body = %s", body)
	}
	if !payload.OK || payload.InstanceID != s.InstanceID || payload.PID != ownPID() ||
		payload.Provider != "codex" || payload.Account != "work" {
		t.Errorf("health = %+v", payload)
	}
}

// Claude Code probes ANTHROPIC_BASE_URL with HEAD / and no credentials.
// 401 on that probe is what the WorkBuddy proxy was reporting as a hard
// auth failure even while POST /v1/messages would succeed.
func TestHandlerRootLivenessDoesNotRequireCredentials(t *testing.T) {
	up := newUpstreamProbe(t)
	s := newTestServer(t, up.url)

	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, _ := http.NewRequest(method, s.Endpoint+"/", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s /: %v", method, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s / status = %d, want 200 (Claude Code liveness)", method, resp.StatusCode)
		}
		if strings.Contains(string(body), s.Token) || strings.Contains(string(body), "upstream-secret") {
			t.Fatalf("%s / leaked a credential: %s", method, body)
		}
		if up.gotPath != "" {
			t.Fatalf("%s / reached upstream at %q", method, up.gotPath)
		}
	}

	req, _ := http.NewRequest(http.MethodHead, s.Endpoint+"/v1/messages", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HEAD /v1/messages = %d, want 401: only / is liveness", resp.StatusCode)
	}
}

func TestServerForwardsWithInjectedCredentialsAndStripsToken(t *testing.T) {
	up := newUpstreamProbe(t)
	s := newTestServer(t, up.url)

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-x"}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if up.gotAuth != "Bearer upstream-secret" {
		t.Errorf("upstream auth = %q", up.gotAuth)
	}
	if strings.Contains(up.gotAuth, s.Token) {
		t.Error("PROXY TOKEN LEAKED TO UPSTREAM")
	}
	if up.gotPath != "/v1/chat/completions" || up.gotPayload != `{"model":"gpt-x"}` {
		t.Errorf("forwarded path=%q payload=%q", up.gotPath, up.gotPayload)
	}
}

func TestServerAcceptsXAPIKeyCredentials(t *testing.T) {
	up := newUpstreamProbe(t)
	s := newTestServer(t, up.url)

	req, _ := http.NewRequest("GET", s.Endpoint+"/v1/models", nil)
	req.Header.Set("x-api-key", s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("x-api-key request status = %d, want 200", resp.StatusCode)
	}
}

func TestServerRejectsWrongAndMissingTokens(t *testing.T) {
	up := newUpstreamProbe(t)
	s := newTestServer(t, up.url)

	for _, tc := range []struct {
		name   string
		header func(*http.Request)
	}{
		{"missing", func(r *http.Request) {}},
		{"wrong-bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }},
		{"empty-bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }},
	} {
		req, _ := http.NewRequest("GET", s.Endpoint+"/v1/models", nil)
		tc.header(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", tc.name, resp.StatusCode)
		}
	}
	if up.gotPath != "" {
		t.Error("unauthenticated request reached upstream")
	}
}

func TestServerHandlesConcurrentFirstRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, upstreamURL)

	const requestCount = 16
	start := make(chan struct{})
	errs := make(chan error, requestCount)
	var wg sync.WaitGroup
	for range requestCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, err := http.NewRequest("GET", s.Endpoint+"/v1/models", nil)
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+s.Token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("status = %d", resp.StatusCode)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	up := newUpstreamProbe(t)
	s := newTestServer(t, up.url)
	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestPickFreePortUsable(t *testing.T) {
	port, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if port <= 0 {
		t.Fatalf("port = %d", port)
	}
	s := &Server{ProviderID: "p", AccountName: "a", InstanceID: MustNewInstanceID(),
		Token: MustNewToken()}
	if err := s.Listen("127.0.0.1", port); err != nil {
		t.Fatalf("listen on picked port: %v", err)
	}
	_ = s.Close()
}

// ---- LeaseStore ----

func testLeaseStore(t *testing.T) *LeaseStore {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &LeaseStore{DB: database.DB}
}

func TestLeaseCRUDRoundTrip(t *testing.T) {
	ls := testLeaseStore(t)
	err := ls.Create(Lease{
		LeaseID: "l1", Provider: "codex", Account: "work",
		Port: 4140, Host: "127.0.0.1", Endpoint: "http://127.0.0.1:4140",
		OwnerPID: ownPID(), InstanceID: "inst-1", BindingRefs: []string{"client/codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ls.Get("l1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Provider != "codex" || got.Account != "work" || got.Port != 4140 ||
		got.InstanceID != "inst-1" || len(got.BindingRefs) != 1 {
		t.Errorf("lease = %+v", got)
	}
	list, _ := ls.ListByAccount("codex", "work")
	if len(list) != 1 {
		t.Errorf("list by account = %d", len(list))
	}
	if err := ls.Delete("l1"); err != nil {
		t.Fatal(err)
	}
	gone, _ := ls.Get("l1")
	if gone != nil {
		t.Error("lease survived delete")
	}
}

func TestReapStaleOnlyRemovesDeadOwnerPIDs(t *testing.T) {
	ls := testLeaseStore(t)

	// A genuinely exited process.
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn probe process: %v", err)
	}
	deadPID := cmd.Process.Pid
	_ = cmd.Wait()

	mustCreate := func(id string, pid int) {
		t.Helper()
		if err := ls.Create(Lease{LeaseID: id, Provider: "p", Port: 1,
			OwnerPID: pid}); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate("own", ownPID())
	mustCreate("dead", deadPID)

	reaped, err := ls.ReapStale()
	if err != nil {
		t.Fatal(err)
	}
	if reaped < 1 {
		t.Fatalf("reaped = %d, want >=1", reaped)
	}
	if own, _ := ls.Get("own"); own == nil {
		t.Error("live-owner lease must survive reaping")
	}
	if dead, _ := ls.Get("dead"); dead != nil {
		t.Error("dead-owner lease must be reaped")
	}
}

// ---- Service ----

type fixedResolver struct {
	up *url.URL
}

func (r *fixedResolver) ResolveUpstream(context.Context, string, string) (*Upstream, error) {
	return &Upstream{BaseURL: r.up, Auth: "upstream-secret"}, nil
}

type testServices struct {
	Service *Service
	Up      *upstreamProbe
}

func newTestServices(t *testing.T) *testServices {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(t.TempDir(), database)
	up := newUpstreamProbe(t)
	svc := New(st, &LeaseStore{DB: database.DB})
	svc.Resolver = &fixedResolver{up: up.url}
	return &testServices{Service: svc, Up: up}
}

func seedAccount(t *testing.T, svc *Service, name string, enabled bool) {
	t.Helper()
	staging, _, err := svc.Store.PrepareSnapshot("codex", name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging+"/auth.json", []byte(`{"OPENAI_API_KEY":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if err := svc.Store.WriteMetaContext(context.Background(), store.Meta{
		Name: name, Provider: "codex", CreatedAt: now, UpdatedAt: now,
	}, staging); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SetProxyConfigContext(context.Background(), "codex", name, store.ProxyConfig{Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.DiscardPreparedSnapshot(staging)
}

func TestStartReusesLiveInstance(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "work", true)

	first, err := ts.Service.StartProxy(ctx, "codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !first.StartedNow {
		t.Fatal("first start must report StartedNow")
	}
	second, err := ts.Service.StartProxy(ctx, "codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if second.StartedNow {
		t.Error("second start must reuse the running instance")
	}
	if second.Endpoint != first.Endpoint || second.Token != first.Token {
		t.Errorf("reuse mismatch: %+v vs %+v", second, first)
	}
	if err := ts.Service.Stop(ctx, "codex", "work"); err != nil {
		t.Fatal(err)
	}
	status, _ := ts.Service.Status("codex", "work")
	if status.Running {
		t.Error("proxy still running after Stop")
	}
}

func TestStopClearsEnabledSoItStaysStopped(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "work", true)

	if _, err := ts.Service.StartProxy(ctx, "codex", "work"); err != nil {
		t.Fatal(err)
	}
	before, err := ts.Service.Store.GetProxyConfigContext(ctx, "codex", "work")
	if err != nil || !before.Enabled || before.Port == 0 {
		t.Fatalf("config after start = %+v, %v", before, err)
	}
	if err := ts.Service.Stop(ctx, "codex", "work"); err != nil {
		t.Fatal(err)
	}
	after, err := ts.Service.Store.GetProxyConfigContext(ctx, "codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled {
		t.Fatalf("config after stop = %+v, want Enabled=false", after)
	}
	if after.Port != before.Port {
		t.Fatalf("stop changed port %d → %d", before.Port, after.Port)
	}
}

// The bearer token is stable per account: client configs written once at
// activation must survive restarts, or every configured app 401s until the
// next activation rewrites it.
func TestProxyTokenStableAcrossRestart(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "work", true)

	first, err := ts.Service.StartProxy(ctx, "codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == "" {
		t.Fatal("first start issued no token")
	}
	if err := ts.Service.Stop(ctx, "codex", "work"); err != nil {
		t.Fatal(err)
	}
	second, err := ts.Service.StartProxy(ctx, "codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if second.Token != first.Token {
		t.Fatal("proxy token rotated across restart: client configs would 401")
	}
	if second.Endpoint != first.Endpoint {
		t.Fatalf("endpoint moved %s → %s", first.Endpoint, second.Endpoint)
	}
}

func TestStartMissingAccountFailsClosed(t *testing.T) {
	ts := newTestServices(t)
	if _, err := ts.Service.StartProxy(context.Background(), "codex", "ghost"); err == nil {
		t.Fatal("expected ACCOUNT_NOT_FOUND")
	}
}

func TestFailedStartRestoresConfigAndRuntimeState(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "failed-start", false)
	ts.Service.Spawner = func(string, string, store.ProxyRuntimeState, string) (int, error) {
		return 0, errors.New("spawn failed")
	}

	if _, err := ts.Service.StartProxy(ctx, "codex", "failed-start"); err == nil {
		t.Fatal("want start failure")
	}
	cfg, err := ts.Service.Store.GetProxyConfigContext(ctx, "codex", "failed-start")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("failed start left mutated config: %+v", cfg)
	}
	state, err := ts.Service.Store.ReadProxyStateContext(ctx, "codex", "failed-start")
	if err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("failed start left runtime state: %+v", state)
	}
}

func TestStopIfLeaseOwnedRequiresExactInstance(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "work", true)

	started, err := ts.Service.StartProxy(ctx, "codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	// Wrong instance id must NOT stop the server.
	if err := ts.Service.StopIfLeaseOwned(ctx, "codex", "work", "other-instance", ""); err != nil {
		t.Fatal(err)
	}
	status, _ := ts.Service.Status("codex", "work")
	if !status.Running {
		t.Fatal("wrong instance stopped a live proxy")
	}
	// Exact instance stops it.
	if err := ts.Service.StopIfLeaseOwned(ctx, "codex", "work", started.InstanceID, started.LeaseID); err != nil {
		t.Fatal(err)
	}
	status, _ = ts.Service.Status("codex", "work")
	if status.Running {
		t.Error("exact-instance stop did not take effect")
	}
}

func TestStopWaitsForProviderMutationLock(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "serialized-stop", true)
	if _, err := ts.Service.StartProxy(ctx, "codex", "serialized-stop"); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- lock.WithMutationLock(
			ctx,
			ts.Service.Store.Root,
			refs.ProviderScope("codex"),
			func(context.Context) error {
				close(locked)
				<-release
				return nil
			},
		)
	}()
	<-locked

	stopDone := make(chan error, 1)
	go func() { stopDone <- ts.Service.Stop(ctx, "codex", "serialized-stop") }()
	select {
	case err := <-stopDone:
		close(release)
		<-holderDone
		t.Fatalf("Stop bypassed the provider mutation lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	status, err := ts.Service.StatusContext(ctx, "codex", "serialized-stop")
	if err != nil {
		close(release)
		<-holderDone
		t.Fatal(err)
	}
	if !status.Running {
		close(release)
		<-holderDone
		t.Fatal("proxy stopped while the provider mutation lock was held")
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
}

func TestStatusNeverExposesToken(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "work", true)

	if _, err := ts.Service.StartProxy(ctx, "codex", "work"); err != nil {
		t.Fatal(err)
	}
	status, _ := ts.Service.Status("codex", "work")
	data, _ := json.Marshal(status)
	state, _ := ts.Service.Store.ReadProxyState("codex", "work")
	if state == nil || state.Token == "" {
		t.Fatal("expected persisted token in proxy_state")
	}
	if strings.Contains(string(data), state.Token) {
		t.Error("STATUS OUTPUT CONTAINS THE PROXY TOKEN")
	}
	if err := ts.Service.Stop(ctx, "codex", "work"); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledConfigAutoEnablesOnStart(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "lazy", false)

	cfg, _ := ts.Service.Store.GetProxyConfig("codex", "lazy")
	if cfg.Enabled {
		t.Fatal("precondition: config starts disabled")
	}
	res, err := ts.Service.StartProxy(ctx, "codex", "lazy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ts.Service.Stop(ctx, "codex", "lazy") }()

	cfg, _ = ts.Service.Store.GetProxyConfig("codex", "lazy")
	if !cfg.Enabled || cfg.Port != res.Port {
		t.Errorf("config after auto-enable = %+v, port %d", cfg, res.Port)
	}
}

func TestConfigureValidatesAndPersistsUnderServiceBoundary(t *testing.T) {
	ts := newTestServices(t)
	ctx := context.Background()
	seedAccount(t, ts.Service, "configured", false)

	cfg, err := ts.Service.Configure(ctx, "codex", "configured", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Port <= 0 || cfg.Port > 65535 {
		t.Fatalf("enabled config = %+v", cfg)
	}
	persisted, err := ts.Service.Store.GetProxyConfigContext(ctx, "codex", "configured")
	if err != nil || persisted.Enabled != cfg.Enabled ||
		persisted.Port != cfg.Port || persisted.Host != cfg.Host {
		t.Fatalf("persisted config = %+v, err = %v", persisted, err)
	}

	if _, err := ts.Service.Configure(ctx, "codex", "configured", true, 65536); err == nil {
		t.Fatal("want invalid port error")
	}
	afterInvalid, err := ts.Service.Store.GetProxyConfigContext(ctx, "codex", "configured")
	if err != nil || afterInvalid.Enabled != cfg.Enabled ||
		afterInvalid.Port != cfg.Port || afterInvalid.Host != cfg.Host {
		t.Fatalf("invalid update mutated config: %+v, err = %v", afterInvalid, err)
	}

	disabled, err := ts.Service.Configure(ctx, "codex", "configured", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.Port != cfg.Port {
		t.Fatalf("disabled config = %+v, want preserved port %d", disabled, cfg.Port)
	}
}
