package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// echoProxy is a real loopback listener standing in for a plugin-hosted proxy:
// its /health echoes one instance id, which lets the Service exercise its
// genuine ownership gates against live HTTP.
type echoProxy struct {
	server     *httptest.Server
	instanceID string
}

func newEchoProxy(t *testing.T, instanceID string) *echoProxy {
	t.Helper()
	ep := &echoProxy{instanceID: instanceID}
	ep.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = fmt.Fprintf(w, `{"instanceId":%q,"pid":%d}`, ep.instanceID, os.Getpid())
	}))
	t.Cleanup(ep.server.Close)
	return ep
}

func (ep *echoProxy) close() { ep.server.Close() }

// fakeLifecycle is a configurable PluginLifecycle double. starts/stops count
// forwarded calls so tests can prove reuse happens without crossing the
// boundary again.
type fakeLifecycle struct {
	forwards map[string]bool
	start    func(ctx context.Context, req PluginRequest) (*PluginStart, error)
	stop     func(req PluginRequest) error
	status   func(req PluginRequest) (*Status, error)
	logs     func(req PluginRequest, lines int) (string, error)
	// terminate models the last-resort runtime kill; terminated records that
	// it ran so tests can prove the unsettled path took it.
	terminate  func() error
	terminated bool
	// terminateExpected records the identities callers demanded, so tests can
	// prove stale generations are refused by the real supervisor elsewhere.
	terminateExpected []PluginRuntimeIdentity
	runtime           PluginRuntimeIdentity

	startCalls int
	stopCalls  int
}

func (f *fakeLifecycle) ForwardsLifecycle(providerID string) bool {
	return f.forwards[providerID]
}

func (f *fakeLifecycle) StartProxy(ctx context.Context, req PluginRequest) (*PluginStart, error) {
	f.startCalls++
	if f.start == nil {
		return nil, errors.New("fakeLifecycle: no start behavior")
	}
	return f.start(ctx, req)
}

func (f *fakeLifecycle) StopProxy(_ context.Context, req PluginRequest) error {
	f.stopCalls++
	if f.stop == nil {
		// Default: start-capable plugin without the optional stop method.
		return ErrPluginMethodAbsent
	}
	return f.stop(req)
}

func (f *fakeLifecycle) ProxyStatus(_ context.Context, req PluginRequest) (*Status, error) {
	if f.status == nil {
		return nil, ErrPluginMethodAbsent
	}
	return f.status(req)
}

func (f *fakeLifecycle) ReadProxyLogs(_ context.Context, req PluginRequest, lines int) (string, error) {
	if f.logs == nil {
		return "", ErrPluginMethodAbsent
	}
	return f.logs(req, lines)
}

func (f *fakeLifecycle) Terminate(_ context.Context, expected PluginRuntimeIdentity) error {
	f.terminateExpected = append(f.terminateExpected, expected)
	f.terminated = true
	if f.terminate == nil {
		return nil
	}
	return f.terminate()
}

func (f *fakeLifecycle) RuntimeIdentity() PluginRuntimeIdentity { return f.runtime }

func newForwardedServices(t *testing.T) (*Service, *fakeLifecycle) {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(t.TempDir(), database)
	svc := New(st, &LeaseStore{DB: database.DB})
	fake := &fakeLifecycle{forwards: map[string]bool{"pluginprov": true}}
	svc.PluginLifecycle = fake
	return svc, fake
}

func seedForwardedAccount(t *testing.T, svc *Service, name string, cfg store.ProxyConfig) {
	t.Helper()
	staging, _, err := svc.Store.PrepareSnapshot("pluginprov", name)
	if err != nil {
		t.Fatal(err)
	}
	// Account visibility requires at least one snapshot file (store invariant).
	if err := os.WriteFile(staging+"/auth.json", []byte(`{"fixture":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if err := svc.Store.WriteMetaContext(context.Background(), store.Meta{
		Name: name, Provider: "pluginprov", CreatedAt: now, UpdatedAt: now,
	}, staging); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SetProxyConfigContext(context.Background(), "pluginprov", name, cfg); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.DiscardPreparedSnapshot(staging)
}

func TestForwardedStartPersistsEngineOwnedLifecycle(t *testing.T) {
	svc, fake := newForwardedServices(t)
	echo := newEchoProxy(t, "inst-forward-1")
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{
			Endpoint: echo.server.URL, Compatibility: "Fixture OpenAI API",
			PID: ownPID(), InstanceID: "inst-forward-1",
		}, nil
	}
	seedForwardedAccount(t, svc, "work", store.ProxyConfig{Enabled: false})

	res, err := svc.StartProxy(context.Background(), "pluginprov", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !res.StartedNow || res.Endpoint != echo.server.URL || res.InstanceID != "inst-forward-1" {
		t.Fatalf("start result = %+v", res)
	}
	if len(res.Token) != 64 {
		t.Fatalf("engine token = %q, want 32-byte hex", res.Token)
	}
	if _, err := hex.DecodeString(res.Token); err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
	state, err := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "work")
	if err != nil || state == nil {
		t.Fatalf("proxy_state = %+v, %v", state, err)
	}
	if state.Token != res.Token || state.InstanceID != res.InstanceID {
		t.Fatalf("persisted state = %+v does not match result %+v", state, res)
	}
	lease, err := svc.Jobs.Get(res.LeaseID)
	if err != nil || lease == nil {
		t.Fatalf("lease %q = %+v, %v", res.LeaseID, lease, err)
	}
	if lease.Account != "work" || lease.InstanceID != "inst-forward-1" {
		t.Fatalf("lease = %+v", lease)
	}
	cfg, err := svc.Store.GetProxyConfigContext(context.Background(), "pluginprov", "work")
	if err != nil || !cfg.Enabled || cfg.Port == 0 {
		t.Fatalf("config after start = %+v, %v", cfg, err)
	}

	// Idempotent start: the recorded instance is adopted through the health
	// echo and the boundary is NOT crossed a second time.
	before := fake.startCalls
	reused, err := svc.StartProxy(context.Background(), "pluginprov", "work")
	if err != nil {
		t.Fatal(err)
	}
	if reused.StartedNow || reused.Token != res.Token || reused.Endpoint != res.Endpoint {
		t.Fatalf("reuse result = %+v, want identical reuse of %+v", reused, res)
	}
	if fake.startCalls != before {
		t.Fatalf("startProxy forwarded %d extra times during reuse", fake.startCalls-before)
	}
}

func TestForwardedStartRejectsUnsafeEndpointAndCompensates(t *testing.T) {
	svc, fake := newForwardedServices(t)
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{Endpoint: "http://10.0.0.9:8080", InstanceID: "inst-x"}, nil
	}
	seedForwardedAccount(t, svc, "unsafe", store.ProxyConfig{Enabled: false})

	_, err := svc.StartProxy(context.Background(), "pluginprov", "unsafe")
	var startErr *anyerr.Error
	if !errors.As(err, &startErr) || startErr.Code != "PROXY_UNSAFE_HOST" {
		t.Fatalf("unsafe endpoint error = %v (%T), want PROXY_UNSAFE_HOST", err, err)
	}
	ctx := context.Background()
	cfg, cerr := svc.Store.GetProxyConfigContext(ctx, "pluginprov", "unsafe")
	if cerr != nil || cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("failed start left mutated config: %+v, %v", cfg, cerr)
	}
	if state, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "unsafe"); state != nil {
		t.Fatalf("failed start left runtime state: %+v", state)
	}
	leases, lerr := svc.Jobs.ListByAccountContext(ctx, "pluginprov", "unsafe")
	if lerr != nil || len(leases) != 0 {
		t.Fatalf("failed start left leases: %+v, %v", leases, lerr)
	}
}

func TestForwardedFailedOwnershipGateCompensatesAndStopsListener(t *testing.T) {
	svc, fake := newForwardedServices(t)
	// The listener echoes an instance id different from the reported one: the
	// Service must refuse ownership and compensate everything.
	echo := newEchoProxy(t, "someone-else")
	reported := "inst-ours"
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{Endpoint: echo.server.URL, PID: ownPID(), InstanceID: reported}, nil
	}
	stoppedViaPlugin := false
	fake.stop = func(PluginRequest) error {
		stoppedViaPlugin = true
		echo.close()
		return nil
	}
	seedForwardedAccount(t, svc, "hijack", store.ProxyConfig{Enabled: false})

	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 200 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	_, err := svc.StartProxy(context.Background(), "pluginprov", "hijack")
	var gateErr *anyerr.Error
	if !errors.As(err, &gateErr) || gateErr.Code != "PROXY_START_FAILED" {
		t.Fatalf("ownership-gate error = %v (%T), want PROXY_START_FAILED", err, err)
	}
	if !stoppedViaPlugin {
		t.Fatal("compensation never asked the plugin to stop its listener")
	}
	ctx := context.Background()
	cfg, _ := svc.Store.GetProxyConfigContext(ctx, "pluginprov", "hijack")
	if cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("failed gate left mutated config: %+v", cfg)
	}
	if state, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "hijack"); state != nil {
		t.Fatalf("failed gate left runtime state: %+v", state)
	}
}

func TestForwardedStopVerifiesTerminationAndFailsClosed(t *testing.T) {
	svc, fake := newForwardedServices(t)
	echo := newEchoProxy(t, "inst-stop-1")
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{Endpoint: echo.server.URL, PID: ownPID(), InstanceID: "inst-stop-1"}, nil
	}
	refusing := true
	fake.stop = func(PluginRequest) error {
		if refusing {
			return nil // Plugin lies: the listener keeps serving.
		}
		echo.close()
		return nil
	}
	seedForwardedAccount(t, svc, "work", store.ProxyConfig{Enabled: true})

	res, err := svc.StartProxy(context.Background(), "pluginprov", "work")
	if err != nil {
		t.Fatal(err)
	}

	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 250 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	err = svc.Stop(context.Background(), "pluginprov", "work")
	var stopErr *anyerr.Error
	if !errors.As(err, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("unverified stop error = %v (%T), want PROXY_STOP_UNVERIFIED", err, err)
	}
	// Fail closed: records survive so the orphan stays diagnosable.
	state, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "work")
	if state == nil || state.Token != res.Token {
		t.Fatalf("unverified stop erased runtime state: %+v", state)
	}
	if lease, _ := svc.Jobs.Get(res.LeaseID); lease == nil {
		t.Fatal("unverified stop released the lease")
	}

	refusing = false
	if err := svc.Stop(context.Background(), "pluginprov", "work"); err != nil {
		t.Fatalf("verified stop = %v", err)
	}
	if state, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "work"); state != nil {
		t.Fatalf("verified stop left runtime state: %+v", state)
	}
	if lease, _ := svc.Jobs.Get(res.LeaseID); lease != nil {
		t.Fatalf("verified stop left lease %+v", lease)
	}
	// Zero surviving listeners.
	if resp, rerr := http.Get(res.Endpoint + "/health"); rerr == nil {
		resp.Body.Close()
		t.Fatal("listener survived a verified stop")
	}
	// Stopping an already-stopped proxy must not fail (TS stopProxy contract).
	if err := svc.Stop(context.Background(), "pluginprov", "work"); err != nil {
		t.Fatalf("second stop = %v, want nil", err)
	}
}

func TestForwardedStopWithoutStopMethodFailsClosedThenDropsStale(t *testing.T) {
	svc, fake := newForwardedServices(t)
	echo := newEchoProxy(t, "inst-nostop")
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{Endpoint: echo.server.URL, PID: ownPID(), InstanceID: "inst-nostop"}, nil
	}
	fake.stop = func(PluginRequest) error {
		return ErrPluginMethodAbsent // start-capable plugin without stopProxy
	}
	seedForwardedAccount(t, svc, "nostop", store.ProxyConfig{Enabled: true})

	res, err := svc.StartProxy(context.Background(), "pluginprov", "nostop")
	if err != nil {
		t.Fatal(err)
	}

	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 250 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	// Live instance + no stopProxy: fail closed instead of orphaning silently.
	err = svc.Stop(context.Background(), "pluginprov", "nostop")
	var stopErr *anyerr.Error
	if !errors.As(err, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("absent-stop error = %v (%T), want PROXY_STOP_UNVERIFIED", err, err)
	}
	if state, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "nostop"); state == nil {
		t.Fatal("fail-closed stop dropped the runtime state")
	}

	// Once the instance echo is gone the records are stale and drop cleanly.
	echo.close()
	if err := svc.Stop(context.Background(), "pluginprov", "nostop"); err != nil {
		t.Fatalf("stale-record stop = %v", err)
	}
	if state, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "nostop"); state != nil {
		t.Fatal("stale runtime state survived")
	}
	if lease, _ := svc.Jobs.Get(res.LeaseID); lease != nil {
		t.Fatalf("stale lease survived: %+v", lease)
	}
}

// startPlannedListener binds the exact host:port the engine planned (the
// contract a well-behaved plugin follows) and echoes one instance id.
func startPlannedListener(t *testing.T, host string, port int, instanceID string) *http.Server {
	t.Helper()
	if host == "" {
		host = "127.0.0.1"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = fmt.Fprintf(w, `{"instanceId":%q,"pid":%d}`, instanceID, os.Getpid())
	})
	server := &http.Server{Addr: net.JoinHostPort(host, strconv.Itoa(port)), Handler: mux}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatalf("bind planned %s: %v", server.Addr, err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func plannedInUse(t *testing.T, host string, port int) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestForwardedBindThenThrowIsCompensated(t *testing.T) {
	svc, fake := newForwardedServices(t)
	// The plugin binds the planned port and THEN rejects — the old assumption
	// that a start error means nothing is bound would orphan it.
	var mu sync.Mutex
	var server *http.Server
	plannedPort := 0
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		plannedPort = req.Config.Port // captured BEFORE compensation restores 0
		server = startPlannedListener(t, req.Config.Host, req.Config.Port, "inst-late")
		return nil, errors.New("late bind failure")
	}
	fake.stop = func(PluginRequest) error {
		mu.Lock()
		defer mu.Unlock()
		if server != nil {
			_ = server.Close()
			server = nil
		}
		return nil
	}
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 300 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "bind-throw", store.ProxyConfig{Enabled: false})

	_, err := svc.StartProxy(context.Background(), "pluginprov", "bind-throw")
	if err == nil || !strings.Contains(err.Error(), "late bind failure") {
		t.Fatalf("bind-then-throw error = %v", err)
	}
	if plannedInUse(t, "", plannedPort) {
		t.Fatal("bound listener survived failed-start compensation")
	}
	ctx := context.Background()
	if state, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "bind-throw"); state != nil {
		t.Fatalf("compensated start left runtime state: %+v", state)
	}
	cfg, _ := svc.Store.GetProxyConfigContext(ctx, "pluginprov", "bind-throw")
	if cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("compensated start left mutated config: %+v", cfg)
	}
}

func TestForwardedCancelledStartStopsLateBindingListener(t *testing.T) {
	svc, fake := newForwardedServices(t)
	// Cancellation returns BEFORE the host coroutine settles: the plugin binds
	// only after observing the abort. The grace window must catch it.
	var mu sync.Mutex
	var server *http.Server
	plannedPort := 0
	startCtx, cancel := context.WithCancel(context.Background())
	fake.start = func(ctx context.Context, req PluginRequest) (*PluginStart, error) {
		plannedPort = req.Config.Port // captured BEFORE compensation restores 0
		<-ctx.Done()
		go func() {
			time.Sleep(40 * time.Millisecond)
			mu.Lock()
			server = startPlannedListener(t, req.Config.Host, req.Config.Port, "inst-cancel-late")
			mu.Unlock()
		}()
		return nil, ctx.Err()
	}
	fake.stop = func(req PluginRequest) error {
		mu.Lock()
		defer mu.Unlock()
		if server != nil {
			_ = server.Close()
			server = nil
		}
		return nil
	}
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = time.Second
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "cancel-late", store.ProxyConfig{Enabled: false})

	done := make(chan error, 1)
	go func() { _, err := svc.StartProxy(startCtx, "pluginprov", "cancel-late"); done <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start error = %v, want context.Canceled", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !plannedInUse(t, "", plannedPort) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grace window never stopped the late-binding listener")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx := context.Background()
	if state, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "cancel-late"); state != nil {
		t.Fatalf("cancelled start left runtime state: %+v", state)
	}
	cfg, _ := svc.Store.GetProxyConfigContext(ctx, "pluginprov", "cancel-late")
	if cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("cancelled start left mutated config: %+v", cfg)
	}
}

func TestForwardedUnverifiableOrphanKeepsDiagnosticRecord(t *testing.T) {
	svc, fake := newForwardedServices(t)
	var server *http.Server
	var plannedPort int
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		plannedPort = req.Config.Port
		server = startPlannedListener(t, req.Config.Host, req.Config.Port, "inst-orphan")
		return nil, errors.New("threw after binding") // handle never returned
	}
	// The plugin's stop is a silent no-op: the listener keeps serving.
	fake.stop = func(PluginRequest) error { return nil }
	seedForwardedAccount(t, svc, "orphan", store.ProxyConfig{Enabled: false})

	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 400 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	_, err := svc.StartProxy(context.Background(), "pluginprov", "orphan")
	var stopErr *anyerr.Error
	if !errors.As(err, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("unprovable orphan error = %v (%T), want PROXY_STOP_UNVERIFIED", err, err)
	}
	ctx := context.Background()

	// The orphan stays diagnosable: a proxy_state row names the planned
	// endpoint (no handle ever came back, so there is no instance id) and the
	// engine token for later adoption or teardown.
	state, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "orphan")
	if serr != nil || state == nil || state.InstanceID != "" ||
		state.Endpoint != fmt.Sprintf("http://127.0.0.1:%d", plannedPort) || state.Token == "" {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	if !plannedInUse(t, "", plannedPort) {
		t.Fatal("orphaned listener unexpectedly gone")
	}
	cfg, _ := svc.Store.GetProxyConfigContext(ctx, "pluginprov", "orphan")
	if cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("config was not restored alongside the diagnostic record: %+v", cfg)
	}
	leases, _ := svc.Jobs.ListByAccountContext(ctx, "pluginprov", "orphan")
	if len(leases) != 0 {
		t.Fatalf("failed start created leases: %+v", leases)
	}

	// Once the orphan actually dies, the stale record drops cleanly.
	_ = server.Close()
	if err := svc.Stop(context.Background(), "pluginprov", "orphan"); err != nil {
		t.Fatalf("stale-record stop = %v", err)
	}
	if state, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "orphan"); state != nil {
		t.Fatal("diagnostic record survived a verified-dead orphan")
	}
}

func TestForwardedOutputBoundariesRedactEngineSecrets(t *testing.T) {
	svc, fake := newForwardedServices(t)
	const leaked = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	seedForwardedAccount(t, svc, "redact", store.ProxyConfig{Enabled: true})
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "redact", store.ProxyRuntimeState{
		AccountName: "redact", Endpoint: "http://127.0.0.1:43112",
		PID: ownPID(), InstanceID: "inst-redact",
		StartedAt: nowISO(), Token: leaked,
	}); err != nil {
		t.Fatal(err)
	}

	// Provider-controlled log text cannot leak the engine token.
	fake.logs = func(PluginRequest, int) (string, error) {
		return "starting\nauthorization: Bearer " + leaked + "\nx-api-key: " + leaked +
			"\ntoken=" + leaked + "\ntail " + leaked, nil
	}
	text, err := svc.ProxyLogs(context.Background(), "pluginprov", "redact", 50)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, leaked) {
		t.Fatal("proxy logs leaked the engine token")
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatalf("credential patterns were not screened: %q", text)
	}

	// Bounded output even for hostile provider text.
	fake.logs = func(PluginRequest, int) (string, error) {
		return strings.Repeat("x", maxRedactedOutput*2) + leaked, nil
	}
	text, err = svc.ProxyLogs(context.Background(), "pluginprov", "redact", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > maxRedactedOutput+64 {
		t.Fatalf("log output exceeded the bound: %d bytes", len(text))
	}
	if strings.Contains(text, leaked) {
		t.Fatal("truncated log output still leaked the token")
	}

	// Status text fields from the provider are screened too.
	fake.status = func(PluginRequest) (*Status, error) {
		return &Status{
			Running: true, Endpoint: "http://127.0.0.1:43113/?tok=" + leaked,
			Compatibility: "compat " + leaked, Detail: "detail bearer " + leaked,
			LogPath: "/tmp/" + leaked + ".log",
		}, nil
	}
	out, err := svc.StatusContext(context.Background(), "pluginprov", "redact")
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, merr := json.Marshal(out)
	if merr != nil {
		t.Fatal(merr)
	}
	if bytes.Contains(statusJSON, []byte(leaked)) {
		t.Fatalf("status JSON leaked the engine token: %s", statusJSON)
	}

	// Forwarded-start errors are part of the output boundary as well.
	var capturedToken string
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		capturedToken = req.Token
		return nil, errors.New("upstream rejected credential=" + req.Token)
	}
	_, startErr := svc.StartProxy(context.Background(), "pluginprov", "redact")
	if startErr == nil {
		t.Fatal("expected start failure")
	}
	if capturedToken != "" && strings.Contains(startErr.Error(), capturedToken) {
		t.Fatal("start error message leaked the generated token")
	}
	if !strings.Contains(startErr.Error(), "[redacted]") {
		t.Fatalf("error not screened: %v", startErr)
	}
}

// TestForwardedUnsettledWithoutScopedRecordFailsClosed proves the P1 rule for
// the no-handle force-termination path: when the terminated runtime leaves no
// scoped ownership record, its children cannot be proven dead — no matter how
// empty the planned socket looks — so the diagnostic record stays behind.
func TestForwardedUnsettledWithoutScopedRecordFailsClosed(t *testing.T) {
	svc, fake := newForwardedServices(t)
	plannedPort := 0
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		plannedPort = req.Config.Port
		// No scoped ownership record is written: nothing is provable.
		return nil, fmt.Errorf("%w: %v", ErrUnsettledInvocation, context.Canceled)
	}
	fake.stop = func(PluginRequest) error { return nil }
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 100 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "unsettled", store.ProxyConfig{Enabled: false})

	_, err := svc.StartProxy(context.Background(), "pluginprov", "unsettled")
	if err == nil {
		t.Fatal("unsettled start must fail loudly")
	}
	if !fake.terminated {
		t.Fatal("unsettled cancellation never terminated the plugin runtime")
	}
	time.Sleep(300 * time.Millisecond) // well past every window

	// Fail closed: without a scoped record the diagnostic row survives.
	ctx := context.Background()
	state, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "unsettled")
	if serr != nil || state == nil || state.Token == "" || state.InstanceID != "" ||
		state.Endpoint != fmt.Sprintf("http://127.0.0.1:%d", plannedPort) {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	var stopErr *anyerr.Error
	if !errors.As(err, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("unprovable teardown error = %v (%T), want PROXY_STOP_UNVERIFIED", err, err)
	}
	cfg, _ := svc.Store.GetProxyConfigContext(ctx, "pluginprov", "unsettled")
	if cfg.Enabled || cfg.Port != 0 {
		t.Fatalf("config not restored alongside the diagnostic record: %+v", cfg)
	}
}

// TestForwardedUnsettledKillsRecordedChildKeepsDiagnostic shows what a scoped
// record buys on the force path: the recorded child is found, strictly
// ownership-verified and stopped — but a verified kill cannot prove its whole
// process tree contained, so every record stays behind.
func TestForwardedUnsettledKillsRecordedChildKeepsDiagnostic(t *testing.T) {
	svc, fake := newForwardedServices(t)
	instanceID := "11111111-1111-4111-8111-111111111111"
	planned, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		child, endpoint, exited := spawnScopedChild(t, instanceID, req.Config.Port, 0)
		writeScopedRecord(t, req, instanceID, child.Process.Pid, endpoint,
			time.Now(), nil)
		go func() {
			<-exited
			close(childDone)
		}()
		return nil, fmt.Errorf("%w: %v", ErrUnsettledInvocation, context.Canceled)
	}
	fake.stop = func(PluginRequest) error { return nil }
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 200 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "provable", store.ProxyConfig{Enabled: false, Port: planned})

	_, startErr := svc.StartProxy(context.Background(), "pluginprov", "provable")
	if startErr == nil {
		t.Fatal("unsettled start must fail loudly")
	}
	if !fake.terminated {
		t.Fatal("termination never ran")
	}
	select {
	case <-childDone:
	case <-time.After(3 * time.Second):
		t.Fatal("recorded child survived compensation")
	}
	ctx := context.Background()
	var stopErr *anyerr.Error
	if !errors.As(startErr, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("unproven-tree teardown error = %v (%T), want PROXY_STOP_UNVERIFIED", startErr, startErr)
	}
	state, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "provable")
	if serr != nil || state == nil || state.Token == "" || state.InstanceID != "" ||
		state.Endpoint != fmt.Sprintf("http://127.0.0.1:%d", planned) {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	assertNoLeasesAndConfigRestored(t, svc, "provable", store.ProxyConfig{Enabled: false, Port: planned})
}

// TestForwardedUnsettledNoHandleCatchesLateDetachedChild is the direct P1
// regression: the detached child binds WELL after the planned socket was first
// observed empty. Only the scoped ownership record stands between this start
// and a permanent orphan.
func TestForwardedUnsettledNoHandleCatchesLateDetachedChild(t *testing.T) {
	svc, fake := newForwardedServices(t)
	instanceID := "22222222-2222-4222-8222-222222222222"
	planned, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		child, endpoint, exited := spawnScopedChild(t, instanceID, req.Config.Port, 250)
		writeScopedRecord(t, req, instanceID, child.Process.Pid, endpoint,
			time.Now(), nil)
		go func() {
			<-exited
			close(childDone)
		}()
		// The planned socket stays empty right now — the old code returned
		// proven=true on exactly this observation.
		return nil, fmt.Errorf("%w: %v", ErrUnsettledInvocation, context.Canceled)
	}
	fake.stop = func(PluginRequest) error { return nil }
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 400 * time.Millisecond // proof window 800ms > 250ms bind delay
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "latechild", store.ProxyConfig{Enabled: false, Port: planned})

	_, startErr := svc.StartProxy(context.Background(), "pluginprov", "latechild")
	if startErr == nil {
		t.Fatal("unsettled start must fail loudly")
	}
	if !fake.terminated {
		t.Fatal("termination never ran")
	}
	select {
	case <-childDone:
	case <-time.After(3 * time.Second):
		t.Fatal("late-binding detached child outlived compensation — empty-socket timing window struck back")
	}
	ctx := context.Background()
	var stopErr *anyerr.Error
	if !errors.As(startErr, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("unproven-tree teardown error = %v (%T), want PROXY_STOP_UNVERIFIED", startErr, startErr)
	}
	state, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "latechild")
	if serr != nil || state == nil || state.Token == "" ||
		state.Endpoint != fmt.Sprintf("http://127.0.0.1:%d", planned) {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	assertNoLeasesAndConfigRestored(t, svc, "latechild", store.ProxyConfig{Enabled: false, Port: planned})
}

// TestForwardedUnsettledAdoptsAndStopsDoubleDetachedGrandchild is the whole-
// tree P1 regression: a HEALTHY recorded parent detaches a delayed double-
// detached grandchild, gets strictly verified and killed, its endpoint goes
// dark — and the grandchild re-binds the SAME identity afterwards. A verified
// pid kill must never clear the record or claim success; the retained record
// lets the hunt adopt the rebinder (same endpoint + instance, new live pid)
// and stop it too, while STILL reporting unproven.
func TestForwardedUnsettledAdoptsAndStopsDoubleDetachedGrandchild(t *testing.T) {
	svc, fake := newForwardedServices(t)
	instanceID := "88888888-8888-4888-8888-888888888888"
	planned, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	var parentPID int
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		child, endpoint, _ := spawnScopedGrandchildParent(t, instanceID, req.Config.Port, 250)
		parentPID = child.Process.Pid
		writeScopedRecord(t, req, instanceID, parentPID, endpoint, time.Now(), nil)
		return nil, fmt.Errorf("%w: %v", ErrUnsettledInvocation, context.Canceled)
	}
	fake.stop = func(PluginRequest) error { return nil }
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 300 * time.Millisecond // proof window 600ms > 250ms grandchild delay
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "grandchild", store.ProxyConfig{Enabled: false, Port: planned})

	_, startErr := svc.StartProxy(context.Background(), "pluginprov", "grandchild")
	if startErr == nil {
		t.Fatal("unsettled start must fail loudly")
	}
	if !fake.terminated {
		t.Fatal("termination never ran")
	}
	if processAlive(parentPID) {
		t.Fatal("recorded parent survived compensation")
	}
	// The adopted grandchild's identity ended up in the retained record; it
	// must be dead by now.
	final := readScopedRecordPID(t, svc, "grandchild")
	if final == parentPID || final <= 0 {
		t.Fatalf("record was not rewritten to the adopted grandchild pid: %d", final)
	}
	if processAlive(final) {
		t.Fatal("double-detached grandchild was adopted into the record but not stopped")
	}
	ctx := context.Background()
	var stopErr *anyerr.Error
	if !errors.As(startErr, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("unproven-tree teardown error = %v (%T), want PROXY_STOP_UNVERIFIED", startErr, startErr)
	}
	state, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "grandchild")
	if serr != nil || state == nil || state.Token == "" {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	// Belt: make sure nothing of this scenario outlives the test.
	_ = syscall.Kill(final, syscall.SIGKILL)
}

// TestReconcileAccountAfterHostLossKeepsProxyOnTransientHealthMiss is the P1
// forgetting regression: account B's detached proxy stalls its FIRST /health
// probe past the client timeout, then recovers. Host death proves nothing
// about a detached child, so one missed echo must never delete B's state and
// lease — only echoes missing across a WHOLE verification window may.
func TestReconcileAccountAfterHostLossKeepsProxyOnTransientHealthMiss(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 600 * time.Millisecond // death window: 1200ms of misses
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	gate := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-gate:
		case <-time.After(5 * time.Second):
		}
		w.Header().Set("content-type", "application/json")
		_, _ = fmt.Fprintf(w, `{"instanceId":"inst-b","pid":%d}`, os.Getpid())
	}))
	t.Cleanup(server.Close)

	state := store.ProxyRuntimeState{
		AccountName: "b", Endpoint: server.URL,
		PID: ownPID(), InstanceID: "inst-b", Token: "tok-b", StartedAt: nowISO(),
	}
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "b", state); err != nil {
		t.Fatal(err)
	}
	lease := seedReconcileLease(t, svc, "b", "inst-b")

	done := make(chan error, 1)
	go func() {
		done <- svc.reconcileAccountAfterHostLoss(
			context.Background(), "pluginprov", "b", state, PluginRuntimeIdentity{})
	}()
	// The first probe times out (750ms client budget) while the gate holds;
	// recovery lands inside the verification window.
	time.Sleep(900 * time.Millisecond)
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("transient-miss reconcile = %v", err)
	}
	got, serr := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "b")
	if serr != nil || got == nil || got.Token != "tok-b" {
		t.Fatalf("live detached proxy forgotten after one missed probe: %+v, %v", got, serr)
	}
	if _, err := svc.Jobs.Get(lease.LeaseID); err != nil || lease == nil {
		t.Fatalf("lease released despite the proxy answering again: %v", err)
	}
}

// TestReconcileAccountAfterHostLossClearsVerifiedDead covers the other half:
// an echo that stays absent for the whole window AND an ownership record that
// classifies the listener as IN-HOST of a runtime process that provably no
// longer exists is reconciled atomically — both the state row and every lease
// go together.
func TestReconcileAccountAfterHostLossClearsVerifiedDead(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 150 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	deadOwner := deadProcessPID(t)
	req := svc.pluginRequest("pluginprov", "dead", store.ProxyConfig{}, "")
	writeScopedRecord(t, req, "inst-dead", deadOwner,
		"http://127.0.0.1:1", time.Now(), func(r map[string]interface{}) { delete(r, "endpoint") })
	state := store.ProxyRuntimeState{
		AccountName: "dead", Endpoint: "http://127.0.0.1:1",
		PID:        deadOwner, // the in-host proxy died with its runtime
		InstanceID: "inst-dead", Token: "tok-dead", StartedAt: nowISO(),
	}
	terminated := PluginRuntimeIdentity{Generation: "dead-host-generation", PID: deadOwner}
	state.PluginRuntimeGeneration = terminated.Generation
	state.PluginRuntimePID = terminated.PID
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "dead", state); err != nil {
		t.Fatal(err)
	}
	seedReconcileLease(t, svc, "dead", "inst-dead")

	if err := svc.reconcileAccountAfterHostLoss(
		context.Background(), "pluginprov", "dead", state, terminated); err != nil {
		t.Fatalf("verified-dead reconcile = %v", err)
	}
	ctx := context.Background()
	if got, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "dead"); got != nil {
		t.Fatalf("verified-dead row survived reconciliation: %+v", got)
	}
	leases, lerr := svc.Jobs.ListByAccountContext(ctx, "pluginprov", "dead")
	if lerr != nil || len(leases) != 0 {
		t.Fatalf("leases survived verified-dead reconciliation: %+v, %v", leases, lerr)
	}
}

// TestReconcileClearIsAtomicWhenLeaseDeleteFails is the failure-injection
// regression for the cleanup commit: a lease DELETE that aborts mid-transaction
// (AFTER the proxy_state DELETE already succeeded inside it) must roll BOTH
// halves back. This sweep enumerates proxy_state, so a half-landed cleanup
// would permanently strand the surviving leases — no later pass revisits an
// account whose row is gone.
func TestReconcileClearIsAtomicWhenLeaseDeleteFails(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 150 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	state := store.ProxyRuntimeState{
		AccountName: "atomic", Endpoint: "http://127.0.0.1:1",
		InstanceID: "inst-atomic", Token: "tok-atomic", StartedAt: nowISO(),
	}
	state.PID = deadProcessPID(t)
	terminated := PluginRuntimeIdentity{Generation: "atomic-host-generation", PID: state.PID}
	state.PluginRuntimeGeneration = terminated.Generation
	state.PluginRuntimePID = terminated.PID
	writeScopedRecord(t,
		svc.pluginRequest("pluginprov", "atomic", store.ProxyConfig{}, ""),
		state.InstanceID, state.PID, state.Endpoint, time.Now(),
		func(r map[string]interface{}) { delete(r, "endpoint") }) // in-host generation
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "atomic", state); err != nil {
		t.Fatal(err)
	}
	lease := seedReconcileLease(t, svc, "atomic", "inst-atomic")

	// Injection: every proxy_leases DELETE raises ABORT inside its transaction,
	// i.e. strictly after the proxy_state delete succeeded within it.
	_, err := svc.Jobs.DB.Exec(
		`CREATE TRIGGER inject_lease_delete_failure BEFORE DELETE ON proxy_leases
		 BEGIN SELECT RAISE(ABORT, 'injected lease delete failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	recErr := svc.reconcileAccountAfterHostLoss(
		context.Background(), "pluginprov", "atomic", state, terminated)
	if recErr == nil || !strings.Contains(recErr.Error(), "injected lease delete failure") {
		t.Fatalf("injected lease-delete failure did not surface: %v", recErr)
	}

	ctx := context.Background()
	got, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "atomic")
	if serr != nil || got == nil || got.Token != "tok-atomic" {
		t.Fatalf("proxy_state lost to a half-failed cleanup: %+v, %v", got, serr)
	}
	survivors, lerr := svc.Jobs.ListByAccountContext(ctx, "pluginprov", "atomic")
	if lerr != nil || len(survivors) != 1 || survivors[0].LeaseID != lease.LeaseID {
		t.Fatalf("leases lost to a half-failed cleanup: %+v, %v", survivors, lerr)
	}

	// Remove the injection; the retry lands both halves in one commit.
	if _, err := svc.Jobs.DB.Exec(`DROP TRIGGER inject_lease_delete_failure`); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileAccountAfterHostLoss(
		ctx, "pluginprov", "atomic", state, terminated); err != nil {
		t.Fatalf("retry reconcile after injected failure = %v", err)
	}
	if got, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "atomic"); got != nil {
		t.Fatalf("verified-dead row survived reconciliation: %+v", got)
	}
	if remaining, _ := svc.Jobs.ListByAccountContext(ctx, "pluginprov", "atomic"); len(remaining) != 0 {
		t.Fatalf("leases survived verified-dead reconciliation: %+v", remaining)
	}
}

// TestStopForwardedClearsAtomicallyWhenLeaseDeleteFails pins P1 #1 on the
// NORMAL stop path — the original bug site. A lease DELETE that aborts
// mid-transaction (AFTER the proxy_state DELETE already succeeded inside it)
// must roll BOTH halves back, exactly like the host-loss path. The previous
// split — ClearProxyStateContext joined with a per-lease delete via errors.Join
// — let a lease-delete error proceed to clear the state row, so the surviving
// leases were stranded: reconciliation enumerates proxy_state, so an account
// whose row is gone is never revisited.
func TestStopForwardedClearsAtomicallyWhenLeaseDeleteFails(t *testing.T) {
	svc, fake := newForwardedServices(t)
	echo := newEchoProxy(t, "inst-stop-atomic")
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{Endpoint: echo.server.URL, PID: ownPID(), InstanceID: "inst-stop-atomic"}, nil
	}
	fake.stop = func(PluginRequest) error {
		echo.close() // verified-then-dark: the listener goes away on stop
		return nil
	}
	seedForwardedAccount(t, svc, "work", store.ProxyConfig{Enabled: true})

	res, err := svc.StartProxy(context.Background(), "pluginprov", "work")
	if err != nil {
		t.Fatal(err)
	}

	oldTimeout := lifecycleVerifyTimeout
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	lifecycleVerifyTimeout = 250 * time.Millisecond

	// Injection: every proxy_leases DELETE raises ABORT inside its transaction,
	// i.e. strictly after the proxy_state delete succeeded within it.
	if _, err := svc.Jobs.DB.Exec(
		`CREATE TRIGGER inject_lease_delete_failure BEFORE DELETE ON proxy_leases
		 BEGIN SELECT RAISE(ABORT, 'injected lease delete failure'); END;`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = svc.Jobs.DB.Exec(`DROP TRIGGER IF EXISTS inject_lease_delete_failure`) }()

	stopErr := svc.Stop(context.Background(), "pluginprov", "work")
	if stopErr == nil || !strings.Contains(stopErr.Error(), "injected lease delete failure") {
		t.Fatalf("injected lease-delete failure did not surface from the stop path: %v", stopErr)
	}

	ctx := context.Background()
	state, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "work")
	if serr != nil || state == nil || state.Token != res.Token {
		t.Fatalf("stop-path half-failure lost the proxy_state row: %+v, %v", state, serr)
	}
	if lease, lerr := svc.Jobs.Get(res.LeaseID); lerr != nil || lease == nil {
		t.Fatalf("stop-path half-failure lost the lease: %+v, %v", lease, lerr)
	}

	// Remove the injection; the retry lands both halves in one commit.
	if _, err := svc.Jobs.DB.Exec(`DROP TRIGGER inject_lease_delete_failure`); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(context.Background(), "pluginprov", "work"); err != nil {
		t.Fatalf("retry stop after injected failure = %v", err)
	}
	if state, _ := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "work"); state != nil {
		t.Fatalf("verified stop left runtime state: %+v", state)
	}
	if lease, _ := svc.Jobs.Get(res.LeaseID); lease != nil {
		t.Fatalf("verified stop left lease %+v", lease)
	}
}

// TestReconcileKeepsPausedDetachedProxyAcrossWholeWindow is the timing-vs-
// death regression: a detached proxy SIGSTOPped longer than the ENTIRE
// verification window misses every health probe exactly like a corpse, but
// its recorded process identity is still alive. Darkness alone must never
// drop its state and leases; after SIGCONT the very same instance serves
// again, still tracked.
func TestReconcileKeepsPausedDetachedProxyAcrossWholeWindow(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 200 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	instanceID := "44444444-4444-4444-8444-444444444444"
	child, endpoint, _ := spawnScopedChild(t, instanceID, 0, 0)
	req := svc.pluginRequest("pluginprov", "paused", store.ProxyConfig{}, "")
	writeScopedRecord(t, req, instanceID, child.Process.Pid, endpoint, time.Now(), nil)

	state := store.ProxyRuntimeState{
		AccountName: "paused", Endpoint: endpoint,
		PID: child.Process.Pid, InstanceID: instanceID,
		Token: "tok-paused", StartedAt: nowISO(),
	}
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "paused", state); err != nil {
		t.Fatal(err)
	}
	lease := seedReconcileLease(t, svc, "paused", instanceID)

	if err := syscall.Kill(child.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// The full window passes dark while the owner process stays frozen.
	if err := svc.reconcileAccountAfterHostLoss(
		context.Background(), "pluginprov", "paused", state, PluginRuntimeIdentity{}); err != nil {
		t.Fatalf("paused-proxy reconcile = %v", err)
	}
	ctx := context.Background()
	got, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "paused")
	if serr != nil || got == nil || got.Token != "tok-paused" || got.InstanceID != instanceID {
		t.Fatalf("SIGSTOPed detached proxy forgotten across the whole window: %+v, %v", got, serr)
	}
	if after, _ := svc.Jobs.Get(lease.LeaseID); after == nil {
		t.Fatal("lease released while the recorded owner process is still alive")
	}

	// Resume: the identical listener comes back, untouched by reconciliation.
	if err := syscall.Kill(child.Process.Pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if err := waitHealthInstance(endpoint, instanceID, 5*time.Second); err != nil {
		t.Fatalf("detached proxy never recovered after SIGCONT: %v", err)
	}
}

// TestReconcileKeepsLateBindingGrandchildAfterRecordedOwnerDies closes the
// whole-tree gap in host-loss reconciliation: a validated scoped record names
// a detached child, that child's pid is PROVABLY dead and reaped, /health
// stays dark for the entire verification window — yet a double-detached
// grandchild can still bind the SAME identity afterwards. A verified pid kill
// never proves tree containment (stopScopedOwnershipChild's own contract), so
// reconciliation may not treat a dead pid as proof either: the row, its token
// and the lease stay behind as the only handles the engine holds over the
// late binder.
func TestReconcileKeepsLateBindingGrandchildAfterRecordedOwnerDies(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 150 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	instanceID := "66666666-6666-4666-8666-666666666666"
	planned, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := (&url.URL{
		Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(planned)),
	}).String()
	req := svc.pluginRequest("pluginprov", "lategrand", store.ProxyConfig{}, "")
	deadParent := deadProcessPID(t) // fully exited and reaped
	writeScopedRecord(t, req, instanceID, deadParent, endpoint, time.Now(), nil)

	state := store.ProxyRuntimeState{
		AccountName: "lategrand", Endpoint: endpoint,
		PID: deadParent, InstanceID: instanceID,
		Token: "tok-lategrand", StartedAt: nowISO(),
	}
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "lategrand", state); err != nil {
		t.Fatal(err)
	}
	lease := seedReconcileLease(t, svc, "lategrand", instanceID)

	// Parent dead, socket empty, whole window dark: exactly the observation
	// that used to authorize the drop.
	if err := svc.reconcileAccountAfterHostLoss(
		context.Background(), "pluginprov", "lategrand", state, PluginRuntimeIdentity{}); err != nil {
		t.Fatalf("dead-detached-owner reconcile = %v", err)
	}
	ctx := context.Background()
	got, serr := svc.Store.ReadProxyStateContext(ctx, "pluginprov", "lategrand")
	if serr != nil || got == nil || got.Token != "tok-lategrand" || got.InstanceID != instanceID {
		t.Fatalf("detached row dropped although tree containment is unprovable: %+v, %v", got, serr)
	}
	if after, _ := svc.Jobs.Get(lease.LeaseID); after == nil {
		t.Fatal("lease dropped alongside an unprovable tree")
	}

	// The double-detached grandchild binds LATE, claiming the recorded
	// identity on the recorded endpoint. The retained record/token/lease are
	// what keep it manageable instead of orphaned.
	_, _, _ = spawnScopedChild(t, instanceID, planned, 0)
	if served := healthInstance(endpoint); served != instanceID {
		t.Fatalf("grandchild bound %q, want the retained identity %q", served, instanceID)
	}
}

// TestReconcileRequiresEngineRuntimeGeneration proves plugin-written record
// shape never classifies a listener as in-host. Only the exact engine-stamped
// generation that was terminated authorizes cleanup; an endpointless or
// otherwise invalid record with the same dead PID remains ambiguous.
func TestReconcileRequiresEngineRuntimeGeneration(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 120 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	dropEndpoint := func(r map[string]interface{}) { delete(r, "endpoint") }
	deadOwner := deadProcessPID(t)

	writeStateAndLease := func(name string, markInHost bool) store.ProxyRuntimeState {
		t.Helper()
		req := svc.pluginRequest("pluginprov", name, store.ProxyConfig{}, "")
		writeScopedRecord(t, req, "inst-"+name, deadOwner,
			"http://127.0.0.1:1", time.Now(), dropEndpoint)
		state := store.ProxyRuntimeState{
			AccountName: name, Endpoint: "http://127.0.0.1:1",
			PID:        deadOwner,
			InstanceID: "inst-" + name, Token: "tok-" + name, StartedAt: nowISO(),
		}
		if markInHost {
			state.PluginRuntimeGeneration = "terminated-generation"
			state.PluginRuntimePID = deadOwner
		}
		if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", name, state); err != nil {
			t.Fatal(err)
		}
		seedReconcileLease(t, svc, name, "inst-"+name)
		return state
	}
	terminated := PluginRuntimeIdentity{Generation: "terminated-generation", PID: deadOwner}

	// The engine-stamped exact runtime generation clears; the endpointless
	// plugin record neither grants nor vetoes that trusted relation.
	if err := svc.reconcileAccountAfterHostLoss(context.Background(), "pluginprov", "reca",
		writeStateAndLease("reca", true), terminated); err != nil {
		t.Fatalf("engine-generation reconcile = %v", err)
	}
	if got, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "reca"); got != nil {
		t.Fatalf("engine-stamped dead generation survived reconciliation: %+v", got)
	}

	// The same endpointless record and dead PID without an engine marker cannot
	// be promoted to in-host ownership by plugin-controlled bytes.
	if err := svc.reconcileAccountAfterHostLoss(context.Background(), "pluginprov", "recb",
		writeStateAndLease("recb", false), terminated); err != nil {
		t.Fatalf("ambiguous-record reconcile = %v", err)
	}
	got, serr := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "recb")
	if serr != nil || got == nil || got.Token != "tok-recb" {
		t.Fatalf("plugin-shaped record bypassed engine generation proof: %+v, %v", got, serr)
	}

	// A marker from another host generation is equally ineligible even when its
	// PID happens to match the terminated host (PID reuse / stale-state case).
	stale := writeStateAndLease("recc", true)
	stale.PluginRuntimeGeneration = "older-generation"
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "recc", stale); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileAccountAfterHostLoss(
		context.Background(), "pluginprov", "recc", stale, terminated); err != nil {
		t.Fatalf("stale-generation reconcile = %v", err)
	}
	if got, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "recc"); got == nil {
		t.Fatal("stale runtime generation was cleared by a later host termination")
	}
}

// TestAdoptReboundScopedChildNeverSignalsBeforePersist pins the adoption
// contract: when persisting the rewritten scoped record fails, the rebinder's
// pid must NOT be signalled and the durable record must still name the old
// owner — an unpersisted pid is nobody's authorization to kill.
func TestAdoptReboundScopedChildNeverSignalsBeforePersist(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 250 * time.Millisecond // hunt window 500ms > 100ms bind delay
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	instanceID := "55555555-5555-4555-8555-555555555555"
	planned, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	req := svc.pluginRequest("pluginprov", "adoptfail",
		store.ProxyConfig{Enabled: true, Host: "127.0.0.1", Port: planned}, "")

	staleOwner := deadProcessPID(t)
	child, endpoint, _ := spawnScopedChild(t, instanceID, planned, 100)
	writeScopedRecord(t, req, instanceID, staleOwner, endpoint, time.Now(), nil)

	// Block persistence before the commit: with the runtime directory
	// read-only, staging the rewritten record fails, so the adopted pid is
	// never committed. The record itself stays readable and intact.
	//
	// This used to occupy proxy.pid.tmp and relied on the staging file having a
	// fixed name. Staging names are randomised now (internal/atomicfile), so
	// the injection removes write permission instead of guessing the path.
	if err := os.Chmod(req.RuntimeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(req.RuntimeDir, 0o700) })

	dark, huntErr := svc.proveTerminatedChildrenDark(context.Background(), req)
	if dark {
		t.Fatal("no-handle termination can never report proven, even on adoption failure")
	}
	var coded *anyerr.Error
	if !errors.As(huntErr, &coded) || coded.Code != "PROXY_STOP_UNVERIFIED" ||
		!strings.Contains(huntErr.Error(), "adopt rebound scoped proxy child") {
		t.Fatalf("persist-failure hunt = %v (%T), want PROXY_STOP_UNVERIFIED naming the failed adoption",
			huntErr, huntErr)
	}
	// Durable ownership still names the OLD pid...
	if got := readScopedRecordPID(t, svc, "adoptfail"); got != staleOwner {
		t.Fatalf("record rewritten despite the persistence failure: %d, want %d", got, staleOwner)
	}
	// ...and the rebinder was never signalled.
	if !processAlive(child.Process.Pid) {
		t.Fatal("unpersisted adopted pid was signalled anyway")
	}
}

// TestReconcileOnHostLossRunsUnderEachAccountsScopes pins the ADR-0009 fix:
// the sweep runs OUTSIDE the failing account's locks and waits for each other
// account's scopes before touching it.
func TestReconcileOnHostLossRunsUnderEachAccountsScopes(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 100 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()

	state := store.ProxyRuntimeState{
		AccountName: "b", Endpoint: "http://127.0.0.1:1",
		InstanceID: "inst-scope", Token: "tok-scope", StartedAt: nowISO(),
	}
	state.PID = deadProcessPID(t)
	terminated := PluginRuntimeIdentity{Generation: "scope-host-generation", PID: state.PID}
	state.PluginRuntimeGeneration = terminated.Generation
	state.PluginRuntimePID = terminated.PID
	writeScopedRecord(t,
		svc.pluginRequest("pluginprov", "b", store.ProxyConfig{}, ""),
		state.InstanceID, state.PID, state.Endpoint, time.Now(),
		func(r map[string]interface{}) { delete(r, "endpoint") }) // in-host shape
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "b", state); err != nil {
		t.Fatal(err)
	}
	seedReconcileLease(t, svc, "b", "inst-scope")

	scopes := []string{refs.ProviderScope("pluginprov"), proxyScope("pluginprov", "b")}
	holderReleased := make(chan struct{})
	go func() {
		defer close(holderReleased)
		_ = lock.WithMutationLocks(context.Background(), svc.Store.Root, scopes,
			func(context.Context) error {
				time.Sleep(400 * time.Millisecond) // hold B's scopes for a while
				return nil
			}, 0)
	}()
	time.Sleep(80 * time.Millisecond) // let the holder take B's scopes

	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- svc.reconcileForwardedOnHostLoss(
			context.Background(), "pluginprov", "a", terminated)
	}()
	select {
	case <-reconcileDone:
		<-holderReleased
		t.Fatal("reconciliation mutated account B without waiting for B's scopes")
	case <-holderReleased:
	}
	if err := <-reconcileDone; err != nil {
		t.Fatalf("reconcile after scope release = %v", err)
	}
	if got, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "b"); got != nil {
		t.Fatalf("verified-dead row not reconciled under B's own scopes: %+v", got)
	}
}

// TestReconcileSkipsSupersededInstance proves the lock-held re-read: when the
// persisted instance changed while the sweep was waiting for scopes, that row
// belongs to a newer lifecycle and is left untouched.
func TestReconcileSkipsSupersededInstance(t *testing.T) {
	svc := newForwardedServiceOnly(t)
	current := newEchoProxy(t, "inst-current")
	state := store.ProxyRuntimeState{
		AccountName: "b", Endpoint: current.server.URL,
		PID: ownPID(), InstanceID: "inst-current", Token: "tok-current", StartedAt: nowISO(),
	}
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "b", state); err != nil {
		t.Fatal(err)
	}
	stale := state
	stale.InstanceID = "inst-stale"
	if err := svc.reconcileAccountAfterHostLoss(
		context.Background(), "pluginprov", "b", stale, PluginRuntimeIdentity{}); err != nil {
		t.Fatalf("superseded-instance reconcile = %v", err)
	}
	got, _ := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "b")
	if got == nil || got.InstanceID != "inst-current" {
		t.Fatalf("superseded row was touched: %+v", got)
	}
}

func newForwardedServiceOnly(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(t.TempDir(), database)
	svc := New(st, &LeaseStore{DB: database.DB})
	fake := &fakeLifecycle{forwards: map[string]bool{"pluginprov": true}}
	svc.PluginLifecycle = fake
	return svc
}

func seedReconcileLease(t *testing.T, svc *Service, account, instanceID string) *Lease {
	t.Helper()
	lease := Lease{
		LeaseID: "lease-" + account + "-" + instanceID, Provider: "pluginprov",
		Account: account, Port: 43999, Host: "127.0.0.1",
		Endpoint: "http://127.0.0.1:43999", OwnerPID: ownPID(), InstanceID: instanceID,
		CreatedAt: nowISO(), UpdatedAt: nowISO(),
	}
	if err := svc.Jobs.CreateContext(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	return &lease
}

// readScopedRecordPID reads the current pid from an account's scoped record.
func readScopedRecordPID(t *testing.T, svc *Service, name string) int {
	t.Helper()
	runtimeDir := paths.AccountProxyRuntimeDir(svc.Store.Root, "pluginprov", name)
	raw, err := os.ReadFile(filepath.Join(runtimeDir, "proxy.pid"))
	if err != nil {
		t.Fatalf("scoped record vanished: %v", err)
	}
	var rec struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || rec.PID <= 0 {
		t.Fatalf("scoped record unusable: %s", raw)
	}
	return rec.PID
}

// deadProcessPID returns the pid of a process that has fully exited and been
// reaped, so processAlive provably reports false for it — the durable-identity
// shape reconciliation needs to authorize dropping a verified-dark row.
func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn exit-immediately helper: %v", err)
	}
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("exit-immediately helper never exited")
	}
	return pid
}

func assertNoLeasesAndConfigRestored(t *testing.T, svc *Service, name string, want store.ProxyConfig) {
	t.Helper()
	ctx := context.Background()
	leases, err := svc.Jobs.ListByAccountContext(ctx, "pluginprov", name)
	if err != nil || len(leases) != 0 {
		t.Fatalf("failed start left leases: %+v, %v", leases, err)
	}
	cfg, _ := svc.Store.GetProxyConfigContext(ctx, "pluginprov", name)
	if cfg.Enabled != want.Enabled || cfg.Port != want.Port {
		t.Fatalf("config not restored: %+v, want %+v", cfg, want)
	}
}

func TestForwardedUnsettledWithFailedTerminationKeepsRecord(t *testing.T) {
	svc, fake := newForwardedServices(t)
	var server *http.Server
	plannedPort := 0
	fake.start = func(_ context.Context, req PluginRequest) (*PluginStart, error) {
		plannedPort = req.Config.Port
		server = startPlannedListener(t, req.Config.Host, req.Config.Port, "inst-stuck")
		return nil, fmt.Errorf("%w: %v", ErrUnsettledInvocation, context.Canceled)
	}
	fake.stop = func(PluginRequest) error { return nil }
	fake.terminate = func() error { return errors.New("kill failed") }
	oldTimeout := lifecycleVerifyTimeout
	lifecycleVerifyTimeout = 120 * time.Millisecond
	defer func() { lifecycleVerifyTimeout = oldTimeout }()
	seedForwardedAccount(t, svc, "stuck", store.ProxyConfig{Enabled: false})

	_, err := svc.StartProxy(context.Background(), "pluginprov", "stuck")
	var stopErr *anyerr.Error
	if !errors.As(err, &stopErr) || stopErr.Code != "PROXY_STOP_UNVERIFIED" {
		t.Fatalf("failed-termination error = %v (%T), want PROXY_STOP_UNVERIFIED", err, err)
	}

	// Fail closed: the diagnostic record names the live orphan.
	state, serr := svc.Store.ReadProxyStateContext(context.Background(), "pluginprov", "stuck")
	if serr != nil || state == nil || !strings.HasPrefix(state.Endpoint, "http://127.0.0.1:") ||
		state.Token == "" {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	if !plannedInUse(t, "", plannedPort) {
		t.Fatal("listener unexpectedly gone while termination failed")
	}
	server.Close()
}

func TestValidatePluginStartRejectsCredentialBearingURLs(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"bare loopback", "http://127.0.0.1:8080", true},
		{"bracket ipv6", "http://[::1]:8080", true},
		{"bare slash", "http://127.0.0.1:8080/", true},
		{"userinfo token", "http://0123456789abcdef0123456789abcdef@127.0.0.1:8080", false},
		{"query parameter", "http://127.0.0.1:8080/?token=0123456789abcdef0123456789abcdef", false},
		{"fragment", "http://127.0.0.1:8080/#0123456789abcdef0123456789abcdef", false},
		{"force query", "http://127.0.0.1:8080/?", false},
		{"plain path token", "http://127.0.0.1:8080/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"percent-encoded path", "http://127.0.0.1:8080/%30%31%32%33%34%35%36%37%38%39abcdef0123456789abcdef0123456789abcdef", false},
		{"non loopback", "http://10.0.0.9:8080", false},
		{"empty instance", "http://127.0.0.1:8080|no-instance", false},
	}
	for _, tc := range cases {
		instanceID := "inst-validate"
		if tc.name == "empty instance" {
			tc.endpoint = strings.SplitN(tc.endpoint, "|", 2)[0]
			instanceID = ""
		}
		err := ValidatePluginStart(&PluginStart{
			Endpoint: tc.endpoint, InstanceID: instanceID, PID: 1,
		})
		if tc.want && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.want && err == nil {
			t.Errorf("%s: endpoint %q was accepted", tc.name, tc.endpoint)
		}
	}
}

func TestForwardedStatusScreensAfterRuntimeOverlay(t *testing.T) {
	svc, fake := newForwardedServices(t)
	const leaked = "aaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccdddd"
	seedForwardedAccount(t, svc, "overlay", store.ProxyConfig{
		Enabled: true, Host: "127.0.0.1", Port: 43120,
	})
	// Persisted state whose raw endpoint carries a credential component and a
	// PID matching the provider report — the exact ordering trap where the
	// overlay previously reintroduced an unscreened value.
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "overlay", store.ProxyRuntimeState{
		AccountName: "overlay",
		Endpoint:    "http://127.0.0.1:43121/?tok=" + leaked,
		PID:         777, InstanceID: "inst-overlay",
		StartedAt: nowISO(), Token: leaked,
	}); err != nil {
		t.Fatal(err)
	}
	fake.status = func(PluginRequest) (*Status, error) {
		return &Status{Running: true, Endpoint: "http://127.0.0.1:43121", PID: 777}, nil
	}

	out, err := svc.StatusContext(context.Background(), "pluginprov", "overlay")
	if err != nil {
		t.Fatal(err)
	}
	encoded, merr := json.Marshal(out)
	if merr != nil {
		t.Fatal(merr)
	}
	if bytes.Contains(encoded, []byte(leaked)) {
		t.Fatalf("status JSON leaked the engine token after overlay: %s", encoded)
	}
	if !strings.Contains(out.Endpoint, "[redacted]") {
		t.Fatalf("overlay endpoint was not screened: %q", out.Endpoint)
	}
	if out.Endpoint != "http://127.0.0.1:43121/?tok=[redacted]" || out.Port != 43121 {
		t.Fatalf("overlay status = %+v", out)
	}
}

func TestForwardedTokenRotatesAfterVerifiedStop(t *testing.T) {
	svc, fake := newForwardedServices(t)
	current := newEchoProxy(t, "inst-rotate")
	fake.start = func(_ context.Context, _ PluginRequest) (*PluginStart, error) {
		return &PluginStart{Endpoint: current.server.URL, PID: ownPID(), InstanceID: "inst-rotate"}, nil
	}
	fake.stop = func(PluginRequest) error {
		// Verified stop: the listener really dies and the record clears, so the
		// next start binds a fresh instance with a freshly generated token.
		current.close()
		current = newEchoProxy(t, "inst-rotate")
		return nil
	}
	seedForwardedAccount(t, svc, "rotate", store.ProxyConfig{Enabled: true})

	first, err := svc.StartProxy(context.Background(), "pluginprov", "rotate")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(context.Background(), "pluginprov", "rotate"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.StartProxy(context.Background(), "pluginprov", "rotate")
	if err != nil {
		t.Fatal(err)
	}
	if !second.StartedNow {
		t.Fatalf("restart after verified stop = %+v, want a fresh start", second)
	}
	if first.Token == second.Token {
		t.Fatal("token did not rotate after a verified stop")
	}
	for _, token := range []string{first.Token, second.Token} {
		if len(token) != 64 {
			t.Fatalf("token %q is not an engine-generated 32-byte hex secret", token)
		}
	}
}

func TestForwardedStatusOverlaysConfiguredBindAddress(t *testing.T) {
	svc, fake := newForwardedServices(t)
	seedForwardedAccount(t, svc, "status", store.ProxyConfig{
		Enabled: true, Host: "127.0.0.1", Port: 43110,
	})

	// Running report whose pid matches nothing persisted: the configured bind
	// address surfaces (TS post-processing parity).
	fake.status = func(PluginRequest) (*Status, error) {
		return &Status{Running: true, Endpoint: "http://127.0.0.1:43999", PID: 777}, nil
	}
	out, err := svc.StatusContext(context.Background(), "pluginprov", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Running || out.Port != 43110 || out.Host != "127.0.0.1" ||
		out.Endpoint != "http://127.0.0.1:43999" {
		t.Fatalf("status = %+v", out)
	}

	// Persisted state matching the reported pid is authoritative for the
	// endpoint while that exact process serves.
	state := store.ProxyRuntimeState{
		AccountName: "status", Endpoint: "http://127.0.0.1:43111",
		PID: 777, InstanceID: "inst-status", StartedAt: nowISO(), Token: "state-token",
	}
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "status", state); err != nil {
		t.Fatal(err)
	}
	out, err = svc.StatusContext(context.Background(), "pluginprov", "status")
	if err != nil {
		t.Fatal(err)
	}
	if out.Endpoint != "http://127.0.0.1:43111" || out.Port != 43111 {
		t.Fatalf("matched-state status = %+v", out)
	}
	encoded, merr := json.Marshal(out)
	if merr != nil {
		t.Fatal(merr)
	}
	if bytes.Contains(encoded, []byte("state-token")) {
		t.Fatal("status JSON leaked the proxy token")
	}

	// Without proxyStatus the Service falls back to the persisted instance echo.
	fake.status = nil
	out, err = svc.StatusContext(context.Background(), "pluginprov", "status")
	if err != nil {
		t.Fatal(err)
	}
	if out.Running || out.Detail != "stopped" {
		t.Fatalf("fallback status = %+v, want stopped heuristic", out)
	}
	echo := newEchoProxy(t, "inst-status")
	state.Endpoint = echo.server.URL
	if err := svc.Store.WriteProxyStateContext(context.Background(), "pluginprov", "status", state); err != nil {
		t.Fatal(err)
	}
	out, err = svc.StatusContext(context.Background(), "pluginprov", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Running || out.Endpoint != echo.server.URL {
		t.Fatalf("healthy fallback status = %+v", out)
	}
}

func TestForwardedLogsRouteThroughProviderThenFallBack(t *testing.T) {
	svc, fake := newForwardedServices(t)
	seedForwardedAccount(t, svc, "logs", store.ProxyConfig{Enabled: true})

	fake.logs = func(req PluginRequest, lines int) (string, error) {
		if lines != 7 || req.Token != "" {
			t.Fatalf("readProxyLogs request = %+v (%d lines)", req, lines)
		}
		return "remote-fixture-log", nil
	}
	text, err := svc.ProxyLogs(context.Background(), "pluginprov", "logs", 7)
	if err != nil || text != "remote-fixture-log" {
		t.Fatalf("forwarded logs = %q, %v", text, err)
	}

	// Optional-method absence falls back to the engine log file.
	fake.logs = nil
	logPath := filepath.Join(paths.ProxyLogPath(svc.Store.Root, "pluginprov", "logs"))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("line-1\nline-2\nline-3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err = svc.ProxyLogs(context.Background(), "pluginprov", "logs", 2)
	if err != nil || text != "line-2\nline-3" {
		t.Fatalf("fallback logs = %q, %v", text, err)
	}
}

const scopedChildEnvInstance = "GO_TEST_SCOPED_CHILD_INSTANCE"

// TestScopedOwnershipChildHelper is the re-exec body of a REAL child process:
// its /health echoes its own pid, so a positive ownership proof names a
// process that actually hosts the responder — never another process's pid
// claimed by a server living inside the test binary. With
// GO_TEST_SCOPED_CHILD_GRANDCHILD_MS set it also spawns a detached double-
// detached grandchild (same instance, same port, delayed bind with retry) so
// tests can prove the engine never treats a verified pid kill as tree proof.
func TestScopedOwnershipChildHelper(t *testing.T) {
	instanceID := os.Getenv(scopedChildEnvInstance)
	if instanceID == "" {
		t.Skip("scoped ownership child helper body")
	}
	port := os.Getenv("GO_TEST_SCOPED_CHILD_PORT")
	if delay := os.Getenv("GO_TEST_SCOPED_CHILD_DELAY_MS"); delay != "" {
		ms, _ := strconv.Atoi(delay)
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if grandchild := os.Getenv("GO_TEST_SCOPED_CHILD_GRANDCHILD_MS"); grandchild != "" {
		// Detached spawn without Wait: the grandchild survives this process
		// being SIGKILLed and re-binds the same identity after a delay.
		grandchildDelay, _ := strconv.Atoi(grandchild)
		cmd := exec.Command(os.Args[0], "-test.run=TestScopedOwnershipChildHelper$")
		cmd.Env = append(os.Environ(),
			scopedChildEnvInstance+"="+instanceID,
			"GO_TEST_SCOPED_CHILD_PORT="+port,
			"GO_TEST_SCOPED_CHILD_DELAY_MS="+grandchild,
			"GO_TEST_SCOPED_CHILD_GRANDCHILD=1",
			fmt.Sprintf("GO_TEST_SCOPED_CHILD_GRANDCHILD_MS=%d", grandchildDelay),
		)
		if err := cmd.Start(); err != nil {
			os.Exit(4)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = fmt.Fprintf(w, `{"instanceId":%q,"pid":%d}`, instanceID, os.Getpid())
	})
	listen := func() (net.Listener, error) {
		return net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	}
	var listener net.Listener
	if os.Getenv("GO_TEST_SCOPED_CHILD_GRANDCHILD") == "" {
		l, err := listen()
		if err != nil {
			os.Exit(3)
		}
		listener = l
	} else {
		// The grandchild re-binds the recorded endpoint only AFTER the parent
		// died; retry until the port frees up instead of failing the scenario.
		deadline := time.Now().Add(15 * time.Second)
		for {
			l, err := listen()
			if err == nil {
				listener = l
				break
			}
			if !time.Now().Before(deadline) {
				os.Exit(3)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	select {} // blocked until the parent kills us
}

// spawnScopedChild starts a real child whose /health echoes ITS OWN pid,
// bound to port (0 picks a free one) after delayMS milliseconds.
func spawnScopedChild(t *testing.T, instanceID string, port, delayMS int) (cmd *exec.Cmd, endpoint string, exited chan error) {
	t.Helper()
	if port == 0 {
		var err error
		port, err = PickFreePort()
		if err != nil {
			t.Fatal(err)
		}
	}
	cmd = exec.Command(os.Args[0], "-test.run=TestScopedOwnershipChildHelper$")
	cmd.Env = append(os.Environ(),
		scopedChildEnvInstance+"="+instanceID,
		"GO_TEST_SCOPED_CHILD_PORT="+strconv.Itoa(port),
		fmt.Sprintf("GO_TEST_SCOPED_CHILD_DELAY_MS=%d", delayMS),
	)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn scoped child helper: %v", err)
	}
	exited = make(chan error, 1)
	reaped := make(chan struct{})
	go func() {
		exited <- cmd.Wait()
		close(reaped)
	}() // reap promptly so liveness probes settle
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})
	endpoint = (&url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}).String()
	if delayMS == 0 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && healthOwnership(endpoint).PID <= 0 {
			time.Sleep(25 * time.Millisecond)
		}
		if healthOwnership(endpoint).PID <= 0 {
			t.Fatal("scoped child helper never became healthy")
		}
	}
	return cmd, endpoint, exited
}

// spawnScopedGrandchildParent starts a healthy child that has already detached
// a grandchild re-binding the same port + instance after grandchildDelayMS.
func spawnScopedGrandchildParent(t *testing.T, instanceID string, port, grandchildDelayMS int) (cmd *exec.Cmd, endpoint string, exited chan error) {
	t.Helper()
	t.Setenv("GO_TEST_SCOPED_CHILD_GRANDCHILD_MS", strconv.Itoa(grandchildDelayMS))
	return spawnScopedChild(t, instanceID, port, 0)
}

func writeScopedRecord(
	t *testing.T,
	req PluginRequest,
	instanceID string,
	pid int,
	endpoint string,
	createdAt time.Time,
	mutate func(map[string]interface{}),
) {
	t.Helper()
	if err := os.MkdirAll(req.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]interface{}{
		"recordVersion": 1,
		"instanceId":    instanceID,
		"pid":           pid,
		"provider":      req.ProviderID,
		"account":       req.AccountName,
		"endpoint":      endpoint,
	}
	if !createdAt.IsZero() {
		record["createdAt"] = createdAt.UTC().Format(time.RFC3339Nano)
	}
	if mutate != nil {
		mutate(record)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(req.RuntimeDir, "proxy.pid"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func scopedTestRequest(t *testing.T, port int) PluginRequest {
	return PluginRequest{
		ProviderID:  "pluginprov",
		AccountName: "testacc",
		RuntimeDir:  filepath.Join(t.TempDir(), "runtime"),
		Config:      store.ProxyConfig{Enabled: true, Host: "127.0.0.1", Port: port},
	}
}

func TestKillScopedProxyChildRequiresPositiveProof(t *testing.T) {
	spawnSentinel := func() *exec.Cmd {
		cmd := exec.Command("sleep", "100")
		if err := cmd.Start(); err != nil {
			cmd = exec.Command("node", "-e", "setInterval(()=>{}, 100000)")
			if err := cmd.Start(); err != nil {
				t.Skip("cannot spawn sentinel process")
			}
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		time.Sleep(50 * time.Millisecond)
		return cmd
	}
	// responderOn plans a listener on a free port; body decides what identity
	// it claims. These servers live INSIDE the test process, so they are only
	// ever used for negative proofs (a positive kill must come from the real
	// child above).
	responderOn := func(t *testing.T, body string) int {
		port, err := PickFreePort()
		if err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(body))
		})
		server := &http.Server{Handler: mux}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })
		return port
	}
	assertAlive := func(t *testing.T, cmd *exec.Cmd) {
		t.Helper()
		if !processAlive(cmd.Process.Pid) {
			t.Fatalf("unrelated sentinel pid %d was signalled without positive ownership proof", cmd.Process.Pid)
		}
	}
	now := time.Now()

	t.Run("verified kill keeps the record and reports unproven tree", func(t *testing.T) {
		instanceID := "11111111-1111-4111-8111-111111111111"
		child, endpoint, exited := spawnScopedChild(t, instanceID, 0, 0)
		req := scopedTestRequest(t, portOfEndpoint(t, endpoint))
		writeScopedRecord(t, req, instanceID, child.Process.Pid, endpoint, now, nil)

		// A matching health proof authorises killing the recorded pid — but it
		// says nothing about processes the child may have detached, so this is
		// deliberately NOT a success and NOT a record clear.
		err := killScopedProxyChild(req, endpoint, instanceID)
		if err == nil || !strings.Contains(err.Error(), "process tree cannot be proven contained") {
			t.Fatalf("verified kill error = %v, want whole-tree fail-closed", err)
		}
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Fatal("verified child was not killed")
		}
		if _, err := os.Stat(filepath.Join(req.RuntimeDir, "proxy.pid")); errors.Is(err, os.ErrNotExist) {
			t.Fatal("ownership record was cleared without whole-tree containment proof")
		}
	})

	t.Run("refuses health responders without a positive pid", func(t *testing.T) {
		for name, body := range map[string]string{
			"zero pid":      `{"instanceId":"22222222-2222-4222-8222-222222222222","pid":0}`,
			"missing pid":   `{"instanceId":"22222222-2222-4222-8222-222222222222"}`,
			"other pid":     fmt.Sprintf(`{"instanceId":"22222222-2222-4222-8222-222222222222","pid":%d}`, 99999),
			"other person":  `{"instanceId":"deadbeef-dead-beef-dead-deadbeefdead","pid":123}`,
			"garbage shape": `<html>not json</html>`,
		} {
			t.Run(name, func(t *testing.T) {
				sentinel := spawnSentinel()
				port := responderOn(t, body)
				req := scopedTestRequest(t, port)
				writeScopedRecord(t, req,
					"22222222-2222-4222-8222-222222222222", sentinel.Process.Pid,
					fmt.Sprintf("http://127.0.0.1:%d", port), now, nil)
				err := killScopedProxyChild(req, plannedEndpointURL(req), "")
				if err == nil {
					t.Fatal("insufficient health proof must return an error, not silence")
				}
				assertAlive(t, sentinel)
			})
		}
	})

	t.Run("binds the record to the invocation's expectations", func(t *testing.T) {
		sentinel := spawnSentinel()
		instanceID := "33333333-3333-4333-8333-333333333333"
		port := responderOn(t, fmt.Sprintf(`{"instanceId":%q,"pid":%d}`, instanceID, sentinel.Process.Pid))
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		req := scopedTestRequest(t, port)
		writeScopedRecord(t, req, instanceID, sentinel.Process.Pid, endpoint, now, nil)

		// Record endpoint matches the planned socket but the caller expected a
		// different handle instance.
		if err := killScopedProxyChild(req, plannedEndpointURL(req), "44444444-4444-4444-8444-444444444444"); err == nil {
			t.Fatal("record instance not matching the expected handle must be rejected")
		}
		assertAlive(t, sentinel)

		// The record points somewhere the engine never requested.
		otherReq := scopedTestRequest(t, port+1)
		writeScopedRecord(t, otherReq, instanceID, sentinel.Process.Pid, endpoint, now, nil)
		if err := killScopedProxyChild(otherReq, plannedEndpointURL(otherReq), ""); err == nil {
			t.Fatal("record endpoint not matching the planned socket must be rejected")
		}
		assertAlive(t, sentinel)
	})

	t.Run("rejects malformed records before any probe", func(t *testing.T) {
		sentinel := spawnSentinel()
		instanceID := "55555555-5555-4555-8555-555555555555"
		port := responderOn(t, fmt.Sprintf(`{"instanceId":%q,"pid":%d}`, instanceID, sentinel.Process.Pid))
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		cases := []struct {
			name   string
			mutate func(map[string]interface{})
		}{
			{"wrong version", func(r map[string]interface{}) { r["recordVersion"] = 2 }},
			{"zero pid", func(r map[string]interface{}) { r["pid"] = 0 }},
			{"wrong provider", func(r map[string]interface{}) { r["provider"] = "otherprov" }},
			{"wrong account", func(r map[string]interface{}) { r["account"] = "otheracc" }},
			{"missing createdAt", func(r map[string]interface{}) { delete(r, "createdAt") }},
			{"stale createdAt", func(r map[string]interface{}) {
				r["createdAt"] = now.Add(-25 * time.Hour).UTC().Format(time.RFC3339Nano)
			}},
			{"future createdAt", func(r map[string]interface{}) { r["createdAt"] = now.Add(time.Hour).UTC().Format(time.RFC3339Nano) }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := scopedTestRequest(t, port)
				writeScopedRecord(t, req, instanceID, sentinel.Process.Pid, endpoint, now, tc.mutate)
				err := killScopedProxyChild(req, plannedEndpointURL(req), "")
				if err == nil {
					t.Fatalf("%s record must be rejected", tc.name)
				}
				assertAlive(t, sentinel)
			})
		}
		// Not JSON at all.
		req := scopedTestRequest(t, port)
		if err := os.MkdirAll(req.RuntimeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(req.RuntimeDir, "proxy.pid"), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := killScopedProxyChild(req, plannedEndpointURL(req), ""); err == nil {
			t.Fatal("corrupt record must be rejected")
		}
		assertAlive(t, sentinel)
	})

	t.Run("silent health means no signal unless death is proven", func(t *testing.T) {
		deadPort, err := PickFreePort()
		if err != nil {
			t.Fatal(err)
		}
		// Health unreachable + recorded process ALIVE: fail closed with an error.
		liveSentinel := spawnSentinel()
		req := scopedTestRequest(t, deadPort)
		writeScopedRecord(t, req,
			"66666666-6666-4666-8666-666666666666", liveSentinel.Process.Pid,
			fmt.Sprintf("http://127.0.0.1:%d", deadPort), now, nil)
		if err := killScopedProxyChild(req, plannedEndpointURL(req), ""); err == nil {
			t.Fatal("live recorded pid without health proof must fail closed")
		}
		assertAlive(t, liveSentinel)

		// Health unreachable + recorded process DEAD: a single dead pid is no
		// longer proof the whole process tree is gone (the recorded child may
		// have detached a grandchild that binds later), so the prover must keep
		// the diagnostic record and report unproven rather than clear it.
		deadCmd := exec.Command("sleep", "100")
		if err := deadCmd.Start(); err != nil {
			t.Skipf("cannot spawn sleeper: %v", err)
		}
		pid := deadCmd.Process.Pid
		_ = deadCmd.Process.Kill()
		_, _ = deadCmd.Process.Wait()
		if processAlive(pid) {
			t.Skip("sleeper pid could not be reaped deterministically")
		}
		req = scopedTestRequest(t, deadPort)
		writeScopedRecord(t, req,
			"77777777-7777-4777-8777-777777777777", pid,
			fmt.Sprintf("http://127.0.0.1:%d", deadPort), now, nil)
		if err := killScopedProxyChild(req, plannedEndpointURL(req), ""); err == nil {
			t.Fatal("dead recorded pid without whole-tree proof must NOT be cleared")
		}
		if _, err := os.Stat(filepath.Join(req.RuntimeDir, "proxy.pid")); errors.Is(err, os.ErrNotExist) {
			t.Fatal("diagnostic record cleared while a detached descendant may still bind")
		}
	})

	t.Run("absent record is a no-op", func(t *testing.T) {
		req := scopedTestRequest(t, 1)
		if err := killScopedProxyChild(req, plannedEndpointURL(req), ""); err != nil {
			t.Fatalf("absent record = %v, want nil", err)
		}
	})
}

func portOfEndpoint(t *testing.T, endpoint string) int {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Port() == "" {
		t.Fatalf("endpoint %q has no port", endpoint)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestBuiltInPathUnaffectedByWiredButDecliningRouter(t *testing.T) {
	ts := newTestServices(t)
	ts.Service.PluginLifecycle = &fakeLifecycle{forwards: map[string]bool{"codex": false}}
	ctx := context.Background()
	seedAccount(t, ts.Service, "builtin", true)

	res, err := ts.Service.StartProxy(ctx, "codex", "builtin")
	if err != nil {
		t.Fatal(err)
	}
	if !res.StartedNow {
		t.Fatalf("builtin start = %+v", res)
	}
	if err := ts.Service.Stop(ctx, "codex", "builtin"); err != nil {
		t.Fatal(err)
	}
}
