package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/store"
)

// seedFixtureAccount creates a saved account for a plugin provider through the
// store boundary so the proxy Service can drive it like any other account.
func seedFixtureAccount(t *testing.T, a *App, name string, cfg store.ProxyConfig) {
	t.Helper()
	staging, _, err := a.Store.PrepareSnapshot("fixture-provider", name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging+"/auth.json", []byte(`{"fixtureToken":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if err := a.Store.WriteMeta(store.Meta{
		Name: name, Provider: "fixture-provider", CreatedAt: now, UpdatedAt: now,
	}, staging); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetProxyConfig("fixture-provider", name, cfg); err != nil {
		t.Fatal(err)
	}
	_ = a.Store.DiscardPreparedSnapshot(staging)
}

// TestReadyForwardsPluginProxyLifecycleThroughService drives start/status/
// logs/stop through the real composition root and the real Node↔Go process
// boundary: the loopback HTTP fixture lives inside the plugin host child.
func TestReadyForwardsPluginProxyLifecycleThroughService(t *testing.T) {
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join(cwd, "..", "pluginhost", "testdata", "full-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(t.TempDir(), "events.log")
	t.Setenv("ANYPICK_PLUGIN_TEST_EVENTS", events)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), fixture)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}

	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeReady()
	ctx := context.Background()

	// Capability comes from startProxy presence on the registered adapter —
	// built-ins stay on the engine path even though the router is wired.
	if !ready.Proxy.PluginLifecycle.ForwardsLifecycle("fixture-provider") {
		t.Fatal("plugin provider with startProxy was not routed to the forwarding path")
	}
	if ready.Proxy.PluginLifecycle.ForwardsLifecycle("codex") {
		t.Fatal("built-in provider must not route through the plugin lifecycle")
	}

	seedFixtureAccount(t, ready, "work", store.ProxyConfig{Enabled: true})
	res, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !res.StartedNow || res.InstanceID == "" || len(res.Token) != 64 {
		t.Fatalf("start result = %+v", res)
	}
	if !strings.HasPrefix(res.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("endpoint %q is not loopback", res.Endpoint)
	}

	// The engine-generated secret reached the plugin (hash-echoed by the
	// fixture) while the ownership identity is proven through /health.
	tokenHash := sha256.Sum256([]byte(res.Token))
	waitForPluginEvent(t, events, fmt.Sprintf("proxy:token:%s", hex.EncodeToString(tokenHash[:])[:16]))
	healthBody := getOK(t, res.Endpoint+"/health", "")
	var health struct {
		InstanceID string `json:"instanceId"`
		PID        int    `json:"pid"`
	}
	if err := json.Unmarshal(healthBody, &health); err != nil {
		t.Fatal(err)
	}
	if health.InstanceID != res.InstanceID {
		t.Fatalf("/health echoed %q, want %q", health.InstanceID, res.InstanceID)
	}

	// Every credentialed route requires the token (ADR 0006).
	unauthorized := getResponse(t, res.Endpoint+"/v1/models", "")
	if unauthorized != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request status = %d, want 401", unauthorized)
	}
	payload := getOK(t, res.Endpoint+"/v1/models", "Bearer "+res.Token)
	if !bytes.Contains(payload, []byte(`"account":"work"`)) {
		t.Fatalf("proxied response = %s", payload)
	}

	// Persisted lifecycle state: state row carries the engine token, the lease
	// preserves instance + owner identity.
	state, err := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "work")
	if err != nil || state == nil || state.Token != res.Token ||
		state.InstanceID != res.InstanceID {
		t.Fatalf("proxy_state = %+v, %v", state, err)
	}
	lease, err := ready.Leases.Get(res.LeaseID)
	if err != nil || lease == nil || lease.Account != "work" || lease.InstanceID != res.InstanceID {
		t.Fatalf("lease = %+v, %v", lease, err)
	}

	// Repeated start reuses the health-verified instance: no second listener,
	// no second forward.
	reused, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "work")
	if err != nil {
		t.Fatal(err)
	}
	if reused.StartedNow || reused.Token != res.Token || reused.Endpoint != res.Endpoint ||
		reused.InstanceID != res.InstanceID {
		t.Fatalf("reuse result = %+v, want identical reuse of %+v", reused, res)
	}
	eventBytes, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	eventsText := string(eventBytes)
	if got := strings.Count(eventsText, "proxy:token:"); got != 1 {
		t.Fatalf("startProxy crossed the plugin boundary %d times, want 1", got)
	}

	// Public surfaces stay secret-free (ADR 0006 invariant #2).
	status, err := ready.Proxy.StatusContext(ctx, "fixture-provider", "work")
	if err != nil || !status.Running || status.Endpoint != res.Endpoint {
		t.Fatalf("status = %+v, %v", status, err)
	}
	statusJSON, merr := json.Marshal(status)
	if merr != nil {
		t.Fatal(merr)
	}
	if bytes.Contains(statusJSON, []byte(res.Token)) {
		t.Fatal("status JSON leaked the proxy token")
	}
	diagnosticsJSON, merr := json.Marshal(ready.PluginDiagnostics(ctx))
	if merr != nil {
		t.Fatal(merr)
	}
	if bytes.Contains(diagnosticsJSON, []byte(res.Token)) {
		t.Fatal("diagnostics leaked the proxy token")
	}
	logs, err := ready.Proxy.ProxyLogs(ctx, "fixture-provider", "work", 50)
	if err != nil || !strings.Contains(logs, "listening on") {
		t.Fatalf("logs = %q, %v", logs, err)
	}
	if strings.Contains(logs, res.Token) {
		t.Fatal("proxy logs leaked the token")
	}

	// Verified stop: the listener really disappears and records are released.
	if err := ready.Proxy.Stop(ctx, "fixture-provider", "work"); err != nil {
		t.Fatal(err)
	}
	if _, herr := getWithClient(res.Endpoint + "/health"); herr == nil {
		t.Fatal("listener survived a verified stop")
	}
	if state, _ := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "work"); state != nil {
		t.Fatalf("stop left runtime state: %+v", state)
	}
	if lease, _ := ready.Leases.Get(res.LeaseID); lease != nil {
		t.Fatalf("stop left lease %+v", lease)
	}
	// Stopping an already-stopped proxy must not fail.
	if err := ready.Proxy.Stop(ctx, "fixture-provider", "work"); err != nil {
		t.Fatalf("second stop = %v", err)
	}
}

// lastEventSuffix returns the text after the last `prefix` occurrence in the
// fixture event log.
func lastEventSuffix(t *testing.T, events, prefix string) string {
	t.Helper()
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	index := strings.LastIndex(text, prefix)
	if index < 0 {
		t.Fatalf("event prefix %q not found in %q", prefix, text)
	}
	rest := text[index+len(prefix):]
	if end := strings.IndexAny(rest, "\r\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

func TestReadyCompensatesBindThenThrowAcrossHostBoundary(t *testing.T) {
	events, ready, closeReady := startForwardingFixtureApp(t)
	defer closeReady()
	ctx := context.Background()

	cfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-throw"},
	}
	seedFixtureAccount(t, ready, "bind-throw", cfg)
	if _, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "bind-throw"); err == nil {
		t.Fatal("bind-then-throw must fail the start")
	}
	instanceID := lastEventSuffix(t, events, "proxy:started:")
	waitForPluginEvent(t, events, "proxy:stopped:"+instanceID)

	assertConfigRestored(t, ready, "bind-throw", cfg)
	assertNoLifecycleRecords(t, ready, "bind-throw")
}

func TestReadyCompensatesCancelledLateBindAcrossHostBoundary(t *testing.T) {
	events, ready, closeReady := startForwardingFixtureApp(t)
	defer closeReady()

	cfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-cancel-late"},
	}
	seedFixtureAccount(t, ready, "late-bind", cfg)

	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ready.Proxy.StartProxy(startCtx, "fixture-provider", "late-bind")
		done <- err
	}()
	waitForPluginEvent(t, events, "proxy:late-hold")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled late bind = %v, want context.Canceled", err)
	}
	// The host coroutine binds only after the abort; compensation must stop it.
	waitForPluginEvent(t, events, "proxy:late-aborted")
	instanceID := lastEventSuffix(t, events, "proxy:started:")
	waitForPluginEvent(t, events, "proxy:stopped:"+instanceID)

	assertConfigRestored(t, ready, "late-bind", cfg)
	assertNoLifecycleRecords(t, ready, "late-bind")
}

func startForwardingFixtureApp(t *testing.T) (events string, ready *App, closeFn func()) {
	t.Helper()
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join(cwd, "..", "pluginhost", "testdata", "full-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	events = filepath.Join(t.TempDir(), "events.log")
	t.Setenv("ANYPICK_PLUGIN_TEST_EVENTS", events)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), fixture)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	ready, closeFn, err = Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ready.PluginRuntime.Loaded) != 1 || len(ready.PluginRuntime.Failures) != 0 {
		closeFn()
		t.Fatalf("plugin runtime = %+v", ready.PluginRuntime)
	}
	return events, ready, closeFn
}

func TestReadyCompensatesFailedAndCancelledForwardedStarts(t *testing.T) {
	configureAppPluginHost(t)
	root := filepath.Join(t.TempDir(), "data")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join(cwd, "..", "pluginhost", "testdata", "full-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(t.TempDir(), "events.log")
	t.Setenv("ANYPICK_PLUGIN_TEST_EVENTS", events)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), fixture)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	ready, closeReady, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeReady()
	ctx := context.Background()

	failCfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "fail-start"},
	}
	seedFixtureAccount(t, ready, "boom", failCfg)
	if _, startErr := ready.Proxy.StartProxy(ctx, "fixture-provider", "boom"); startErr == nil {
		t.Fatal("fail-start hook did not fail the start")
	}
	assertConfigRestored(t, ready, "boom", failCfg)
	assertNoLifecycleRecords(t, ready, "boom")

	holdCfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "hold-start"},
	}
	seedFixtureAccount(t, ready, "hold", holdCfg)
	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, startErr := ready.Proxy.StartProxy(startCtx, "fixture-provider", "hold")
		done <- startErr
	}()
	waitForPluginEvent(t, events, "proxy:hold")
	cancel()
	startErr := <-done
	if !errors.Is(startErr, context.Canceled) {
		t.Fatalf("cancelled forwarded start = %v, want context.Canceled", startErr)
	}
	waitForPluginEvent(t, events, "proxy:hold-aborted")
	assertConfigRestored(t, ready, "hold", holdCfg)
	assertNoLifecycleRecords(t, ready, "hold")
}

func assertConfigRestored(t *testing.T, ready *App, name string, want store.ProxyConfig) {
	t.Helper()
	cfg, err := ready.Store.GetProxyConfigContext(context.Background(), "fixture-provider", name)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled != want.Enabled || cfg.Port != want.Port ||
		cfg.Options["fixture"] != want.Options["fixture"] {
		t.Fatalf("config after failed start = %+v, want restored %+v", cfg, want)
	}
}

func assertNoLifecycleRecords(t *testing.T, ready *App, name string) {
	t.Helper()
	ctx := context.Background()
	if state, _ := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", name); state != nil {
		t.Fatalf("failed start left runtime state: %+v", state)
	}
	leases, err := ready.Leases.ListByAccountContext(ctx, "fixture-provider", name)
	if err != nil || len(leases) != 0 {
		t.Fatalf("failed start left leases: %+v, %v", leases, err)
	}
}

func getOK(t *testing.T, url, authorization string) []byte {
	t.Helper()
	status, body := getWithAuthorization(url, authorization)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d (%s)", url, status, body)
	}
	return body
}

func getResponse(t *testing.T, url, authorization string) int {
	t.Helper()
	status, _ := getWithAuthorization(url, authorization)
	return status
}

func getWithClient(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func getWithAuthorization(url, authorization string) (int, []byte) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil
	}
	if authorization != "" {
		req.Header.Set("authorization", authorization)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, body
}

func TestReadyOvershootLateBindRequiresTermination(t *testing.T) {
	events, ready, closeReady := startForwardingFixtureApp(t)
	defer closeReady()
	cfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-cancel-late-overshoot"},
	}
	seedFixtureAccount(t, ready, "overshoot", cfg)
	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ready.Proxy.StartProxy(startCtx, "fixture-provider", "overshoot")
		done <- err
	}()
	waitForPluginEvent(t, events, "proxy:late-hold")
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("overshoot cancel = %v, want context.Canceled", err)
	}
	// The plugin binds 700ms after abort, past the 500ms host settle budget.
	// Host must have sent settled=false, Service must have terminated the
	// runtime, and the late bind must never materialise.
	time.Sleep(900 * time.Millisecond)
	if data, _ := os.ReadFile(events); strings.Contains(string(data), "proxy:started:") {
		// Count only overshoot account starts: should be zero
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "proxy:started:") {
				t.Fatalf("overshoot late bind unexpectedly succeeded: %q", line)
			}
		}
	}
	assertConfigRestored(t, ready, "overshoot", cfg)
	// Fail closed (P1): the host died before any scoped ownership record could
	// exist, so darkness is unprovable and the diagnostic record stays behind
	// for later adoption or teardown.
	ctx := context.Background()
	state, serr := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "overshoot")
	if serr != nil || state == nil || state.Token == "" || state.InstanceID != "" ||
		!strings.HasPrefix(state.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("overshoot diagnostic record = %+v, %v", state, serr)
	}
	diag := ready.PluginDiagnostics(context.Background())
	if diag.Host.State != "degraded" && diag.Host.State != "inactive" {
		t.Fatalf("host should be degraded after unsettled termination, got %q", diag.Host.State)
	}
}

// TestReadyUnsettledFailureSparesOtherAccountsDetachedChild pins the blast
// radius of a force-terminated start: the shared plugin runtime dies, but an
// unrelated account's DETACHED child (identified by its own scoped record)
// must survive untouched — its echo, state row and lease all intact.
func TestReadyUnsettledFailureSparesOtherAccountsDetachedChild(t *testing.T) {
	events, ready, closeReady := startForwardingFixtureApp(t)
	defer closeReady()
	ctx := context.Background()

	// Account B: healthy proxy served by a detached child with its own record.
	bCfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-child-detached"},
	}
	seedFixtureAccount(t, ready, "neighbor", bCfg)
	resB, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "neighbor")
	if err != nil {
		t.Fatal(err)
	}
	bRuntime := filepath.Join(ready.Root, "providers", "fixture-provider", "accounts", "neighbor", "runtime")
	childPID := scopedRecordPID(t, bRuntime)
	t.Cleanup(func() {
		if processAlive(childPID) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	// Account A: cancellation whose coroutine never settles → termination.
	aCfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-cancel-late-overshoot"},
	}
	seedFixtureAccount(t, ready, "doomed", aCfg)
	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ready.Proxy.StartProxy(startCtx, "fixture-provider", "doomed")
		done <- err
	}()
	// Cancel only once the coroutine is parked: an earlier cancel can win the
	// race before the host registers the operation, which would let it settle
	// normally instead of taking the force-termination path.
	waitForPluginEvent(t, events, "proxy:late-hold")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("doomed start = %v, want context.Canceled", err)
	}

	// B's detached child keeps serving: A's compensation never touched it.
	if !processAlive(childPID) {
		t.Fatal("account A's failure killed account B's detached child")
	}
	var health struct {
		InstanceID string `json:"instanceId"`
	}
	body := getOK(t, resB.Endpoint+"/health", "")
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatal(err)
	}
	if health.InstanceID != resB.InstanceID {
		t.Fatalf("neighbor /health echoed %q, want %q", health.InstanceID, resB.InstanceID)
	}
	stateB, serr := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "neighbor")
	if serr != nil || stateB == nil || stateB.Token != resB.Token || stateB.InstanceID != resB.InstanceID {
		t.Fatalf("neighbor runtime state = %+v, %v", stateB, serr)
	}
	if lease, _ := ready.Leases.Get(resB.LeaseID); lease == nil {
		t.Fatal("neighbor lease was released by another account's failure")
	}

	// A itself fails closed: no scoped record ever existed for the doomed
	// account, so darkness cannot be proven and the diagnostic row stays.
	stateA, aerr := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "doomed")
	if aerr != nil || stateA == nil || stateA.Token == "" || stateA.InstanceID != "" ||
		!strings.HasPrefix(stateA.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("doomed diagnostic record = %+v, %v", stateA, aerr)
	}
	assertConfigRestored(t, ready, "doomed", aCfg)
}

// TestReadyUnsettledFailureSparesOtherAccountsInHostChild closes the gap the
// detached-child cross-account test skipped: account B's proxy runs IN-HOST
// (the default fixture path), so it lives inside the shared Node host process
// and inherits its process group. When account A's unsettled cancellation
// force-terminates that host, B's listener dies with it — and the engine must
// reconcile B's now-orphaned proxy_state and lease rather than leave them
// reporting running.
func TestReadyUnsettledFailureSparesOtherAccountsInHostChild(t *testing.T) {
	events, ready, closeReady := startForwardingFixtureApp(t)
	defer closeReady()
	ctx := context.Background()

	// Account B: a proxy served IN-HOST (default fixture path), not detached.
	bCfg := store.ProxyConfig{Enabled: false}
	seedFixtureAccount(t, ready, "neighbor", bCfg)
	resB, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "neighbor")
	if err != nil {
		t.Fatal(err)
	}
	if resB.InstanceID == "" || resB.Endpoint == "" {
		t.Fatalf("neighbor start result = %+v", resB)
	}

	// Account A: cancellation whose coroutine never settles → termination of
	// the shared host, which also kills B's in-host listener.
	aCfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-cancel-late-overshoot"},
	}
	seedFixtureAccount(t, ready, "doomed", aCfg)
	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ready.Proxy.StartProxy(startCtx, "fixture-provider", "doomed")
		done <- err
	}()
	waitForPluginEvent(t, events, "proxy:late-hold")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("doomed start = %v, want context.Canceled", err)
	}

	// B's in-host listener is gone with the host. Reconciliation must clear its
	// now-orphaned state and release its lease so it stops reporting running
	// while its listener is already dead.
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, _ := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "neighbor")
		leases, _ := ready.Leases.ListByAccountContext(ctx, "fixture-provider", "neighbor")
		if state == nil && len(leases) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("neighbor proxy_state/leases not reconciled after host loss: state=%+v leases=%+v",
				state, leases)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The host itself should be degraded after the forced termination.
	diag := ready.PluginDiagnostics(context.Background())
	if diag.Host.State != "degraded" && diag.Host.State != "inactive" {
		t.Fatalf("host should be degraded after unsettled termination, got %q", diag.Host.State)
	}
	assertConfigRestored(t, ready, "doomed", aCfg)
}

func TestReadyUnsettledFailureKillsLateBindingDetachedChild(t *testing.T) {
	events, ready, closeReady := startForwardingFixtureApp(t)
	defer closeReady()
	ctx := context.Background()
	cfg := store.ProxyConfig{
		Enabled: false,
		Options: map[string]interface{}{"fixture": "bind-child-detached-abort"},
	}
	seedFixtureAccount(t, ready, "detached-late", cfg)

	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ready.Proxy.StartProxy(startCtx, "fixture-provider", "detached-late")
		done <- err
	}()
	waitForPluginEvent(t, events, "proxy:detached-hold")
	cancel()
	// The child detaches on abort; its spawn event confirms the record exists.
	waitForPluginEvent(t, events, "proxy:detached-spawned:")
	spawned := lastEventSuffix(t, events, "proxy:detached-spawned:")
	pidParts := strings.SplitN(spawned, ":", 2)
	childPID, perr := strconv.Atoi(pidParts[0])
	if perr != nil {
		t.Fatalf("spawn event %q did not carry a pid", spawned)
	}
	if !processAlive(childPID) {
		t.Fatal("detached child was not alive after spawn")
	}
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unsettled detached-abort start = %v, want context.Canceled", err)
	}

	// The child binds ~1.5s after the abort, long after every timing window.
	// Only the scoped ownership record identifies it; compensation must find
	// it there, prove ownership through the health echo, and kill it. The
	// pre-fix code cleared all state while the socket still looked empty and
	// this child stayed behind as a permanent orphan.
	deadline := time.Now().Add(15 * time.Second)
	for processAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatal("late-binding detached child outlived compensation")
		}
		time.Sleep(50 * time.Millisecond)
	}

	runtimeDir := filepath.Join(ready.Root, "providers", "fixture-provider", "accounts", "detached-late", "runtime")
	plannedPort := plannedPortFromFixtureLog(t, runtimeDir)
	assertConfigRestored(t, ready, "detached-late", cfg)
	// Whole-tree fail closed: the child was found through its record and
	// verifiably stopped, but a verified pid kill cannot prove the tree
	// contained, so the diagnostic row stays behind (token, planned endpoint,
	// no instance id) instead of being cleared.
	state, serr := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "detached-late")
	if serr != nil || state == nil || state.Token == "" || state.InstanceID != "" ||
		state.Endpoint != fmt.Sprintf("http://127.0.0.1:%d", plannedPort) {
		t.Fatalf("diagnostic record = %+v, %v", state, serr)
	}
	diag := ready.PluginDiagnostics(context.Background())
	if diag.Host.State != "degraded" && diag.Host.State != "inactive" {
		t.Fatalf("host should be degraded after unsettled termination, got %q", diag.Host.State)
	}
	conn, derr := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(plannedPort)), 250*time.Millisecond)
	if derr == nil {
		_ = conn.Close()
		t.Fatalf("planned socket %d is still accepting connections", plannedPort)
	}
}

// plannedPortFromFixtureLog recovers the port the fixture was told to bind
// from its runtime log ("will bind 127.0.0.1:<port> late").
func plannedPortFromFixtureLog(t *testing.T, runtimeDir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runtimeDir, "proxy.log"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`will bind 127\.0\.0\.1:(\d+) late`).FindSubmatch(raw)
	if match == nil {
		t.Fatalf("fixture log has no planned bind line: %q", raw)
	}
	port, cerr := strconv.Atoi(string(match[1]))
	if cerr != nil {
		t.Fatal(cerr)
	}
	return port
}

// scopedRecordPID reads the detached child's pid out of the account's scoped
// ownership record.
func scopedRecordPID(t *testing.T, runtimeDir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runtimeDir, "proxy.pid"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.PID <= 0 {
		t.Fatalf("scoped record %s has no usable pid", raw)
	}
	return record.PID
}
