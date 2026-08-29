// Post-activation plugin-host recovery/restart regressions (GO-PARITY-M7).
//
// Every test here drives the REAL Node↔Go boundary through the composition
// root; fakes appear only as deterministic barriers and failure injection.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/pluginhost"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transport"
)

// readyFixtureApp boots a data root with the full fixture plugin enabled,
// returning the ready app, its closer, and the fixture event log path.
func readyFixtureApp(t *testing.T) (*App, func(), string) {
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
	return ready, closeReady, events
}

func writeKeepPlugin(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anypick.plugin.json"), []byte(
		`{"name":"keep-plugin","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte(`export default {
  activate(context) {
    context.registerCatalogProvider({
      id: 'keep-catalog',
      name: 'Keep Catalog',
      description: 'Survives sibling activation failure',
      apiStyle: 'openai',
    });
  },
};
`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readyFixtureAndKeepApp(t *testing.T) (*App, func()) {
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
	keepDir := filepath.Join(t.TempDir(), "keep-plugin")
	writeKeepPlugin(t, keepDir)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), fixture)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	if err == nil {
		record, err = bootstrap.Plugins.Add(context.Background(), keepDir)
	}
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
	return ready, closeReady
}

func writeStealPlugin(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anypick.plugin.json"), []byte(
		`{"name":"steal-plugin","version":"1.0.0","apiVersion":1,"main":"index.mjs"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte(`export default {
  activate(context) {
    context.registerCatalogProvider({
      id: 'steal-catalog',
      name: 'Steal Catalog',
      description: 'Owns steal-catalog unless recovery claims another id',
      apiStyle: 'openai',
    });
    const claim = process.env.ANYPICK_PLUGIN_TEST_CLAIM_CATALOG;
    if (claim) {
      context.registerCatalogProvider({
        id: claim,
        name: 'Stolen Catalog',
        description: 'Cross-plugin takeover attempt',
        apiStyle: 'openai',
      });
    }
  },
};
`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readyFixtureAndStealApp(t *testing.T) (*App, func()) {
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
	stealDir := filepath.Join(t.TempDir(), "steal-plugin")
	writeStealPlugin(t, stealDir)

	bootstrap, closeBootstrap, err := Ready(context.Background(), Options{Root: root, SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := bootstrap.Plugins.Add(context.Background(), fixture)
	if err == nil {
		_, err = bootstrap.Plugins.SetEnabled(context.Background(), record.Name, true)
	}
	if err == nil {
		record, err = bootstrap.Plugins.Add(context.Background(), stealDir)
	}
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
	return ready, closeReady
}

func appendSupervisorEnv(t *testing.T, sup *pluginSupervisor, extra ...string) {
	t.Helper()
	sup.mu.Lock()
	defer sup.mu.Unlock()
	if sup.config == nil {
		t.Fatal("plugin supervisor has no host config")
	}
	cfg := *sup.config
	cfg.ExtraEnv = append(append([]string(nil), cfg.ExtraEnv...), extra...)
	sup.config = &cfg
}

// cancelOvershootStart drives a start whose coroutine keeps running past
// cancellation, synchronized through the fixture event log so the abort lands
// while THIS invocation is parked (the deterministic unsettled verdict).
func cancelOvershootStart(t *testing.T, a *App, events, account string) error {
	t.Helper()
	parkedBefore := countEventLines(t, events, "proxy:late-hold")
	startCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.Proxy.StartProxy(startCtx, "fixture-provider", account)
		done <- err
	}()
	waitForCondition(t, account+" parked at late-hold", 5*time.Second, func() bool {
		return countEventLines(t, events, "proxy:late-hold") > parkedBefore
	})
	cancel()
	return <-done
}

func countEventLines(t *testing.T, events, needle string) int {
	t.Helper()
	data, err := os.ReadFile(events)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == needle {
			count++
		}
	}
	return count
}

func waitForCondition(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition %q not met within %s", what, timeout)
}

// liveNodeHosts counts OS processes running the plugin-host script. Best
// effort: skipped where ps is unavailable.
func liveNodeHosts(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ps-based process sweep is unix-only")
	}
	out, err := exec.Command("ps", "-Ao", "args").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "plugin-host.mjs") && !strings.Contains(line, "ps -Ao") {
			count++
		}
	}
	return count
}

// TestConcurrentRecoveryStartsExactlyOneHost: N callers hitting a dead host
// must produce exactly ONE replacement generation; every caller either gets a
// working adapter or shares the same recovery error.
func TestConcurrentRecoveryStartsExactlyOneHost(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	ctx := context.Background()
	sup := ready.pluginSup
	identityA := sup.Identity()

	pv, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pv.DetectLive(); err != nil {
		t.Fatalf("pre-crash detectLive: %v", err)
	}

	// Host-loss trigger: explicit termination (the unsettled-start path ends
	// here in production).
	if err := sup.TerminateGeneration(identityA); err != nil {
		t.Fatalf("terminate A: %v", err)
	}

	const callers = 8
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			current, err := ready.Providers.Get("fixture-provider")
			if err != nil {
				results <- err
				return
			}
			live, err := current.DetectLive()
			if err == nil && (!live.Present || live.Identity != "fixture@example.test") {
				err = fmt.Errorf("unexpected live status %+v", live)
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	startsAfterRecovery := sup.StartCount()
	if startsAfterRecovery != 1 {
		t.Fatalf("concurrent recovery spawned %d hosts, want exactly 1", startsAfterRecovery)
	}
	var failures []error
	for err := range results {
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		t.Fatalf("%d/%d callers failed after recovery: %v", len(failures), callers, failures)
	}

	identityB := sup.Identity()
	if identityB.Generation == identityA.Generation || identityB.PID == identityA.PID {
		t.Fatalf("replacement identity %v did not advance past %v", identityB, identityA)
	}
	diagnostics := ready.PluginDiagnostics(ctx)
	if diagnostics.Host.State != "running" || diagnostics.Host.ActivationCount != 1 {
		t.Fatalf("post-recovery diagnostics = %+v", diagnostics)
	}
	// The old host is gone; only one node host process remains.
	waitForCondition(t, "single live node host", 2*time.Second, func() bool {
		return liveNodeHosts(t) == 1
	})
}

// TestRecoveryReactivatesPluginsWithoutDuplicateRegistrations: after a host
// loss every registration kind reactivates exactly once onto the new host,
// retained handles belong to that generation, and built-ins stay untouched.
func TestRecoveryReactivatesPluginsWithoutDuplicateRegistrations(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()

	oldProvider, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	oldClient, err := ready.Clients.Get("fixture-client")
	if err != nil {
		t.Fatal(err)
	}
	providerIDsBefore := ready.Providers.IDs()
	clientIDsBefore := ready.Clients.IDs()

	if err := ready.pluginSup.TerminateGeneration(ready.pluginSup.Identity()); err != nil {
		t.Fatalf("terminate original host: %v", err)
	}

	// First invocation after the loss drives the single-flight recovery.
	freshProvider, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatalf("provider vanished instead of being replaced: %v", err)
	}
	live, err := freshProvider.DetectLive()
	if err != nil || !live.Present {
		t.Fatalf("recovered detectLive = %+v, %v", live, err)
	}
	// The registry now hands out the replacement generation's adapter.
	freshProvider, err = ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}

	// Exactly one entry per id — RecoverSwap replaces by CAS, never appends.
	assertSingleID := func(what string, ids []string, id string) {
		t.Helper()
		count := 0
		for _, candidate := range ids {
			if candidate == id {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s contains %d entries for %q (%v)", what, count, id, ids)
		}
	}
	assertSingleID("provider registry", ready.Providers.IDs(), "fixture-provider")
	assertSingleID("client registry", ready.Clients.IDs(), "fixture-client")
	for _, builtin := range providerIDsBefore {
		if !ready.Providers.Has(builtin) {
			t.Fatalf("built-in provider %q was lost during recovery", builtin)
		}
	}
	for _, builtin := range clientIDsBefore {
		if !ready.Clients.Has(builtin) {
			t.Fatalf("built-in client %q was lost during recovery", builtin)
		}
	}
	if _, ok := ready.Runtime.Catalog.Lookup("fixture-catalog"); !ok {
		t.Fatal("plugin catalog provider missing after recovery")
	}
	compatible, err := ready.Providers.Get("fixture-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := compatible.(*provider.Compatible); !ok {
		t.Fatalf("compatible provider type %T", compatible)
	}

	// Retained handles belong to the NEW generation: sourceAdapter retention
	// and nested transportFor resolve against the replacement host.
	if freshProvider == oldProvider {
		t.Fatal("registry still hands out the previous generation's adapter")
	}
	adapter := freshProvider.AccountAdapter("work", false)
	if got := adapter.TransportFor("fixture-client"); got != transport.Direct {
		t.Fatalf("recovered transportFor = %q, want direct", got)
	}
	if oldClient == nil || oldClient.ID() != "fixture-client" {
		t.Fatal("client snapshot invalid")
	}
	recoveredClient, err := ready.Clients.Get("fixture-client")
	if err != nil {
		t.Fatal(err)
	}
	if recoveredClient == oldClient {
		t.Fatal("client registry still hands out the previous generation's adapter")
	}
	inspect, err := recoveredClient.Inspect()
	if err != nil {
		t.Fatalf("recovered client inspect: %v", err)
	}
	_ = inspect
	diagnostics := ready.PluginDiagnostics(context.Background())
	if diagnostics.Host.State != "running" || diagnostics.Host.ActivationCount != 1 ||
		len(diagnostics.Failures) != 0 {
		t.Fatalf("post-recovery diagnostics = %+v", diagnostics)
	}
	if ready.pluginSup.StartCount() != 1 {
		t.Fatalf("recovery spawned %d hosts, want 1", ready.pluginSup.StartCount())
	}
}

// TestCloseRacingRecoveryDoesNotRespawn: recovery parked at a deterministic
// barrier must abort once Close begins — no new Node process, no leaked
// goroutines, clean close.
func TestCloseRacingRecoveryDoesNotRespawn(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)

	before := runtime.NumGoroutine()
	if err := ready.pluginSup.TerminateGeneration(ready.pluginSup.Identity()); err != nil {
		t.Fatalf("terminate host: %v", err)
	}

	entered := make(chan struct{})
	barrier := make(chan struct{})
	sup := ready.pluginSup
	sup.mu.Lock()
	sup.hookBeforeSpawn = func() {
		close(entered)
		<-barrier
	}
	sup.mu.Unlock()

	recoveryErr := make(chan error, 1)
	go func() {
		pv, err := ready.Providers.Get("fixture-provider")
		if err != nil {
			recoveryErr <- err
			return
		}
		_, err = pv.DetectLive()
		recoveryErr <- err
	}()

	<-entered // recovery is parked between the closing check and the spawn

	// Start Close while recovery is parked: Close sets closing=true and then
	// blocks draining the in-flight restart. Releasing the barrier afterwards
	// proves the parked spawn aborts instead of racing past Close.
	closeDone := make(chan struct{})
	go func() {
		closeReady()
		close(closeDone)
	}()
	time.Sleep(250 * time.Millisecond)
	close(barrier)

	select {
	case <-closeDone:
	case <-time.After(15 * time.Second):
		t.Fatal("close did not finish after releasing the recovery barrier")
	}
	select {
	case err := <-recoveryErr:
		if !errors.Is(err, ErrRecoveryUnavailable) || !errors.Is(err, ErrAppClosing) {
			t.Fatalf("recovery after close = %v, want ErrAppClosing inside ErrRecoveryUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not finish after close")
	}
	if sup.StartCount() != 0 {
		t.Fatalf("close raced recovery into spawning %d new hosts", sup.StartCount())
	}
	// Subsequent invocations keep refusing without spawning.
	pv, err := ready.Providers.Get("fixture-provider")
	if err == nil {
		if _, err = pv.DetectLive(); !errors.Is(err, ErrAppClosing) &&
			!errors.Is(err, pluginhost.ErrNotDispatched) {
			t.Fatalf("post-close invocation = %v", err)
		}
	}
	if sup.StartCount() != 0 {
		t.Fatalf("post-close invocation spawned %d hosts", sup.StartCount())
	}
	waitForCondition(t, "no live node host after close", 3*time.Second, func() bool {
		return liveNodeHosts(t) == 0
	})
	waitForCondition(t, "goroutines settle after close", 3*time.Second, func() bool {
		return runtime.NumGoroutine() <= before+4
	})
}

// TestReplacementInvisibleUntilGenerationPublished: staged B adapters must
// not be Get()-visible while Identity is still A. After the publish
// boundary, Get and TerminateGeneration agree on B.
func TestReplacementInvisibleUntilGenerationPublished(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	identityA := sup.Identity()
	oldProvider, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}

	if err := sup.TerminateGeneration(identityA); err != nil {
		t.Fatalf("terminate A: %v", err)
	}

	entered := make(chan struct{})
	barrier := make(chan struct{})
	sup.mu.Lock()
	sup.hookBeforePublish = func() {
		close(entered)
		<-barrier
	}
	sup.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := oldProvider.DetectLive()
		done <- err
	}()
	<-entered

	mid, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if mid != oldProvider {
		t.Fatal("registry exposed a replacement adapter before generation publish")
	}
	if got := sup.Identity(); got.Generation != identityA.Generation {
		t.Fatalf("identity advanced to %v before publish", got)
	}
	if err := sup.TerminateGeneration(proxy.PluginRuntimeIdentity{
		Generation: "not-yet-published", PID: 1,
	}); err == nil {
		t.Fatal("unpublished generation was accepted for termination")
	}

	close(barrier)
	if err := <-done; err != nil {
		t.Fatalf("recovery after publish: %v", err)
	}
	fresh, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == oldProvider {
		t.Fatal("registry still hands out generation A after publish")
	}
	identityB := sup.Identity()
	if identityB.Generation == identityA.Generation {
		t.Fatal("identity did not advance at the publish boundary")
	}
	if err := sup.TerminateGeneration(identityB); err != nil {
		t.Fatalf("TerminateGeneration(B) after publish: %v", err)
	}
}

// TestReadersNeverObserveMixedGenerationDuringPublish parks AFTER provider
// swaps and BEFORE client/catalog swaps. Readers that take Get/Lookup/Identity
// during that window must block until the whole generation is visible.
func TestReadersNeverObserveMixedGenerationDuringPublish(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	identityA := sup.Identity()
	oldProvider, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	oldClient, err := ready.Clients.Get("fixture-client")
	if err != nil {
		t.Fatal(err)
	}

	if err := sup.TerminateGeneration(identityA); err != nil {
		t.Fatalf("terminate A: %v", err)
	}

	entered := make(chan struct{})
	barrier := make(chan struct{})
	sup.mu.Lock()
	sup.hookDuringPublish = func() {
		close(entered)
		<-barrier
	}
	sup.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := oldProvider.DetectLive()
		done <- err
	}()
	<-entered

	providerCh := make(chan provider.Provider, 1)
	clientCh := make(chan client.Adapter, 1)
	catalogCh := make(chan bool, 1)
	identityCh := make(chan proxy.PluginRuntimeIdentity, 1)
	go func() {
		p, _ := ready.Providers.Get("fixture-provider")
		providerCh <- p
	}()
	go func() {
		c, _ := ready.Clients.Get("fixture-client")
		clientCh <- c
	}()
	go func() {
		_, ok := ready.Runtime.Catalog.Lookup("fixture-catalog")
		catalogCh <- ok
	}()
	go func() {
		identityCh <- sup.Identity()
	}()

	time.Sleep(250 * time.Millisecond)
	select {
	case p := <-providerCh:
		t.Fatalf("provider Get returned during mixed-generation window (A=%v)", p == oldProvider)
	default:
	}
	select {
	case c := <-clientCh:
		t.Fatalf("client Get returned during mixed-generation window (A=%v)", c == oldClient)
	default:
	}
	select {
	case ok := <-catalogCh:
		t.Fatalf("catalog Lookup returned during mixed-generation window (ok=%v)", ok)
	default:
	}
	select {
	case id := <-identityCh:
		t.Fatalf("Identity returned during mixed-generation window: %v", id)
	default:
	}

	close(barrier)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("recovery after publish: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("recovery did not finish after releasing the publish barrier")
	}
	select {
	case p := <-providerCh:
		if p == nil || p == oldProvider {
			t.Fatal("provider Get still returned generation A after publish")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider Get stayed blocked after the publication write lock dropped")
	}
	select {
	case c := <-clientCh:
		if c == nil || c == oldClient {
			t.Fatal("client Get still returned generation A after publish")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client Get stayed blocked after the publication write lock dropped")
	}
	select {
	case ok := <-catalogCh:
		if !ok {
			t.Fatal("catalog Lookup missed generation B")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog Lookup stayed blocked after the publication write lock dropped")
	}
	select {
	case id := <-identityCh:
		if id.Generation == identityA.Generation {
			t.Fatal("Identity still reported generation A after publish")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Identity stayed blocked after the publication write lock dropped")
	}
}

// TestRecoveryHonoursCallerCancellation: a short-deadline caller must return
// when its ctx ends even if it is the recovery leader; the single-flight
// restart continues for everyone else.
func TestRecoveryHonoursCallerCancellation(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	pv, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	usage, ok := pv.(provider.LiveUsageProvider)
	if !ok {
		t.Fatalf("provider %T lacks usage", pv)
	}
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	entered := make(chan struct{})
	barrier := make(chan struct{})
	sup.mu.Lock()
	sup.hookBeforeSpawn = func() {
		close(entered)
		<-barrier
	}
	sup.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = usage.LiveUsage(ctx)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("cancelled recovery = %v, want deadline", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("caller waited for recovery budget: %s", elapsed)
	}
	<-entered
	if sup.StartCount() != 0 {
		t.Fatal("cancelled caller caused a spawn before the leader finished")
	}

	close(barrier)
	waitForCondition(t, "recovery completes for other callers", 10*time.Second, func() bool {
		current, gerr := ready.Providers.Get("fixture-provider")
		if gerr != nil {
			return false
		}
		_, gerr = current.DetectLive()
		return gerr == nil && sup.Identity().Generation != "" && sup.StartCount() == 1
	})
}

// TestCloseWaitsForInFlightSpawnPermit: Close cannot set closing (and
// therefore cannot "begin") while a restart holds the spawn permit between
// the closing check and pluginhost.Start.
func TestCloseWaitsForInFlightSpawnPermit(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	sup := ready.pluginSup
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	entered := make(chan struct{})
	barrier := make(chan struct{})
	sup.mu.Lock()
	sup.hookBeforeStart = func() {
		close(entered)
		<-barrier
	}
	sup.mu.Unlock()

	recoveryErr := make(chan error, 1)
	go func() {
		pv, err := ready.Providers.Get("fixture-provider")
		if err != nil {
			recoveryErr <- err
			return
		}
		_, err = pv.DetectLive()
		recoveryErr <- err
	}()
	<-entered

	closeDone := make(chan struct{})
	go func() {
		closeReady()
		close(closeDone)
	}()
	time.Sleep(250 * time.Millisecond)
	select {
	case <-closeDone:
		t.Fatal("Close finished while the spawn permit was still held")
	default:
	}
	sup.mu.Lock()
	closing := sup.closing
	sup.mu.Unlock()
	if closing {
		t.Fatal("Close set closing after the spawn check but before Start")
	}
	if sup.StartCount() != 0 {
		t.Fatal("Start ran before the spawn-permit barrier was released")
	}

	close(barrier)
	select {
	case <-closeDone:
	case <-time.After(15 * time.Second):
		t.Fatal("close did not finish after releasing the spawn permit")
	}
	select {
	case <-recoveryErr:
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not finish after close")
	}
	waitForCondition(t, "no live node host after linearized close", 3*time.Second, func() bool {
		return liveNodeHosts(t) == 0
	})
}

// TestTerminateOldGenerationCannotKillReplacement: a stale generation
// reference is refused instead of killing the replacement other accounts now
// depend on; the CURRENT reference still terminates deliberately.
func TestTerminateOldGenerationCannotKillReplacement(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	identityA := sup.Identity()

	if err := sup.TerminateGeneration(identityA); err != nil {
		t.Fatalf("terminate A: %v", err)
	}
	pv, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pv.DetectLive(); err != nil {
		t.Fatalf("recovery after terminate: %v", err)
	}
	identityB := sup.Identity()
	if identityB.Generation == identityA.Generation {
		t.Fatal("restart reused the terminated generation id")
	}

	staleRefusal := sup.TerminateGeneration(identityA)
	if staleRefusal == nil {
		t.Fatal("stale generation reference was accepted — it would have killed B")
	}
	if !strings.Contains(staleRefusal.Error(), identityA.Generation) {
		t.Fatalf("refusal does not name the stale generation: %v", staleRefusal)
	}
	after, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := after.DetectLive(); err != nil {
		t.Fatalf("replacement stopped serving after stale termination: %v", err)
	}
	if sup.Identity().Generation != identityB.Generation {
		t.Fatal("supervisor advanced generations on a refused termination")
	}

	// Positive control: terminating the CURRENT generation still works.
	if err := sup.TerminateGeneration(identityB); err != nil {
		t.Fatalf("terminate current generation: %v", err)
	}
	diagnostics := ready.PluginDiagnostics(context.Background())
	if diagnostics.Host.State != "degraded" {
		t.Fatalf("diagnostics after deliberate termination = %+v", diagnostics)
	}
}

// TestUncertainMutationIsNotReplayed: a mutating call dispatched to a host
// that dies before responding runs EXACTLY ONCE; the caller receives a
// structured uncertain error while recovery still restores a healthy host.
func TestUncertainMutationIsNotReplayed(t *testing.T) {
	ready, closeReady, events := readyFixtureApp(t)
	defer closeReady()

	pv, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	loginer, ok := pv.(provider.Loginer)
	if !ok {
		t.Fatalf("provider %T lacks login capability", pv)
	}
	err = loginer.Login(context.Background(), "mutate-die", "")
	if !errors.Is(err, ErrUncertainMutation) {
		t.Fatalf("ambiguous mutation error = %v, want ErrUncertainMutation chain", err)
	}
	if count := countEventLines(t, events, "login:mutate-die"); count != 1 {
		t.Fatalf("mutation executed %d times, want exactly 1", count)
	}
	if ready.pluginSup.StartCount() != 1 {
		t.Fatalf("uncertain mutation triggered %d spawns, want the single recovery", ready.pluginSup.StartCount())
	}

	// Recovery succeeded inside the failing call: the next read sees healthy.
	current, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.DetectLive(); err != nil {
		t.Fatalf("read-only call after uncertain mutation: %v", err)
	}
	if count := countEventLines(t, events, "login:mutate-die"); count != 1 {
		t.Fatalf("mutation replayed during recovery: %d executions", count)
	}
	diagnostics := ready.PluginDiagnostics(context.Background())
	if diagnostics.Host.State != "running" {
		t.Fatalf("diagnostics after uncertain mutation + recovery = %+v", diagnostics)
	}
}

// TestReadOnlyCallMayRecoverOnce: a read-only call recovers exactly once and
// succeeds; when the replacement cannot start, the caller fails fast with a
// structured degraded error instead of looping.
func TestReadOnlyCallMayRecoverOnce(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	identityA := sup.Identity()

	if err := sup.TerminateGeneration(identityA); err != nil {
		t.Fatalf("terminate A: %v", err)
	}
	pv, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	usageProvider, ok := pv.(provider.LiveUsageProvider)
	if !ok {
		t.Fatalf("provider %T lacks usage capability", pv)
	}
	usage, err := usageProvider.LiveUsage(context.Background())
	if err != nil || usage == nil || len(usage.Windows) != 1 {
		t.Fatalf("read-only recovery result = %+v, %v", usage, err)
	}
	if sup.StartCount() != 1 {
		t.Fatalf("read-only call spawned %d hosts, want 1", sup.StartCount())
	}

	// Failure injection: the NEXT replacement cannot start. One caller gets
	// exactly one attempt — no retry loop.
	sup.mu.Lock()
	sup.hookStart = func(context.Context) (*pluginhost.Host, error) {
		return nil, errors.New("injected restart failure")
	}
	sup.mu.Unlock()
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate B: %v", err)
	}
	started := time.Now()
	_, err = usageProvider.LiveUsage(context.Background())
	elapsed := time.Since(started)
	if !errors.Is(err, ErrRecoveryUnavailable) || !strings.Contains(err.Error(), "injected restart failure") {
		t.Fatalf("failed-recovery error = %v, want wrapped injected cause", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("recovery looped instead of failing fast: %s", elapsed)
	}
	if sup.StartCount() != 2 {
		t.Fatalf("failing recovery made %d spawn attempts, want exactly 1 per caller", sup.StartCount()-1)
	}

	// Injection removed: a NEW caller may attempt one more recovery.
	sup.mu.Lock()
	sup.hookStart = nil
	sup.mu.Unlock()
	usage, err = usageProvider.LiveUsage(context.Background())
	if err != nil || usage == nil {
		t.Fatalf("post-injection recovery = %+v, %v", usage, err)
	}
	if sup.StartCount() != 3 {
		t.Fatalf("expected one more single-flight spawn, total=%d", sup.StartCount())
	}
}

func TestFailedReactivationRemovesDeadRegistrations(t *testing.T) {
	ready, closeReady := readyFixtureAndKeepApp(t)
	defer closeReady()
	sup := ready.pluginSup
	if !ready.Providers.Has("fixture-provider") {
		t.Fatal("fixture provider missing before recovery")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("keep-catalog"); !ok {
		t.Fatal("keep-plugin catalog missing before recovery")
	}

	old, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	appendSupervisorEnv(t, sup, "ANYPICK_PLUGIN_TEST_FAIL_ACTIVATE=1")
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	_, err = old.DetectLive()
	if !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("failed reactivation error = %v, want ErrRecoveryUnavailable", err)
	}
	if ready.Providers.Has("fixture-provider") {
		t.Fatal("failed plugin left generation-A provider in the registry")
	}
	if ready.Clients.Has("fixture-client") {
		t.Fatal("failed plugin left generation-A client in the registry")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("fixture-catalog"); ok {
		t.Fatal("failed plugin left generation-A catalog in the registry")
	}
	if ready.Providers.Has("fixture-compatible") {
		t.Fatal("failed plugin left compatible provider in the registry")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("keep-catalog"); !ok {
		t.Fatal("successful sibling plugin catalog was removed")
	}
	if !ready.Providers.Has("claude") {
		t.Fatal("built-in provider was removed")
	}
	if sup.StartCount() != 1 {
		t.Fatalf("failed reactivation spawned %d hosts, want 1", sup.StartCount())
	}
	diagnostics := ready.PluginDiagnostics(context.Background())
	if diagnostics.Host.State != "running" || diagnostics.Host.LoadedPlugins != 1 {
		t.Fatalf("post-failure diagnostics = %+v", diagnostics)
	}
	foundFailure := false
	for _, failure := range diagnostics.Failures {
		if failure.Name == "fixture-plugin" {
			foundFailure = true
			break
		}
	}
	if !foundFailure {
		t.Fatalf("fixture activation failure missing from diagnostics: %+v", diagnostics.Failures)
	}
}

func TestOmittedRegistrationsAreRemovedOnRecovery(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	old, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if !ready.Clients.Has("fixture-client") {
		t.Fatal("fixture client missing before omit recovery")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("fixture-catalog"); !ok {
		t.Fatal("fixture catalog missing before omit recovery")
	}

	appendSupervisorEnv(t, sup, "ANYPICK_PLUGIN_TEST_OMIT=provider,client,catalog")
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	_, err = old.DetectLive()
	if !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("omitted-provider recovery error = %v, want ErrRecoveryUnavailable", err)
	}
	if ready.Providers.Has("fixture-provider") {
		t.Fatal("omitted provider id remained in the registry")
	}
	if ready.Clients.Has("fixture-client") {
		t.Fatal("omitted client id remained in the registry")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("fixture-catalog"); ok {
		t.Fatal("omitted catalog id remained in the registry")
	}
	if _, ok := ready.Deps.Catalog.Lookup("fixture-catalog"); ok {
		t.Fatal("omitted deps catalog id remained")
	}
	compatible, err := ready.Providers.Get("fixture-compatible")
	if err != nil {
		t.Fatalf("retained compatible provider vanished: %v", err)
	}
	if _, ok := compatible.(*provider.Compatible); !ok {
		t.Fatalf("compatible provider type %T", compatible)
	}
	if !ready.Providers.Has("claude") {
		t.Fatal("built-in provider was removed")
	}
	if sup.StartCount() != 1 {
		t.Fatalf("omit recovery spawned %d hosts, want 1", sup.StartCount())
	}
}

func TestCrossPluginTakeoverDegradesOnlyOffender(t *testing.T) {
	ready, closeReady := readyFixtureAndStealApp(t)
	defer closeReady()
	sup := ready.pluginSup
	if _, ok := ready.Runtime.Catalog.Lookup("steal-catalog"); !ok {
		t.Fatal("steal-plugin catalog missing before recovery")
	}
	old, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}

	appendSupervisorEnv(t, sup, "ANYPICK_PLUGIN_TEST_CLAIM_CATALOG=fixture-catalog")
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if _, err := old.DetectLive(); err != nil {
		t.Fatalf("owner plugin did not recover independently: %v", err)
	}
	if !ready.Providers.Has("fixture-provider") {
		t.Fatal("owner provider vanished after sibling conflict")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("fixture-catalog"); !ok {
		t.Fatal("owner catalog was taken or removed")
	}
	if _, ok := ready.Runtime.Catalog.Lookup("steal-catalog"); ok {
		t.Fatal("failed steal-plugin left its previous catalog registration")
	}
	if !ready.Providers.Has("claude") {
		t.Fatal("built-in provider was removed")
	}
	diagnostics := ready.PluginDiagnostics(context.Background())
	if diagnostics.Host.State != "running" {
		t.Fatalf("host did not stay running: %+v", diagnostics)
	}
	foundSteal := false
	for _, failure := range diagnostics.Failures {
		if failure.Name == "steal-plugin" && strings.Contains(failure.Reason, "already registered") {
			foundSteal = true
			break
		}
	}
	if !foundSteal {
		t.Fatalf("steal-plugin conflict missing from diagnostics: %+v", diagnostics.Failures)
	}
	if diagnostics.Host.LoadedPlugins != 1 {
		t.Fatalf("loaded plugins = %d, want only the owner", diagnostics.Host.LoadedPlugins)
	}
}

func TestPublicationGateReleasedBeforeHostCleanup(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	sup := ready.pluginSup
	if err := sup.TerminateGeneration(sup.Identity()); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	enteredPublish := make(chan struct{})
	releasePublish := make(chan struct{})
	enteredCleanup := make(chan struct{})
	releaseCleanup := make(chan struct{})
	sup.mu.Lock()
	sup.hookBeforePublish = func() {
		close(enteredPublish)
		<-releasePublish
	}
	sup.hookBeforeHostCleanup = func() {
		close(enteredCleanup)
		<-releaseCleanup
	}
	sup.mu.Unlock()

	recoveryErr := make(chan error, 1)
	go func() {
		pv, err := ready.Providers.Get("fixture-provider")
		if err != nil {
			recoveryErr <- err
			return
		}
		_, err = pv.DetectLive()
		recoveryErr <- err
	}()
	<-enteredPublish

	closeDone := make(chan struct{})
	go func() {
		closeReady()
		close(closeDone)
	}()
	waitForCondition(t, "close set closing", 2*time.Second, func() bool {
		sup.mu.Lock()
		defer sup.mu.Unlock()
		return sup.closing
	})
	close(releasePublish)
	<-enteredCleanup

	got := make(chan error, 1)
	go func() {
		_, err := ready.Providers.Get("claude")
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("built-in Get during host cleanup: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseCleanup)
		t.Fatal("publication gate still held during replacement host cleanup")
	}
	close(releaseCleanup)

	select {
	case <-closeDone:
	case <-time.After(15 * time.Second):
		t.Fatal("close did not finish after cleanup")
	}
	select {
	case err := <-recoveryErr:
		if !errors.Is(err, ErrAppClosing) && !errors.Is(err, ErrRecoveryUnavailable) {
			t.Fatalf("recovery after close = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not finish after close")
	}
}

func TestOversizedMutationIsLocalRejectionNotRecovery(t *testing.T) {
	ready, closeReady, _ := readyFixtureApp(t)
	defer closeReady()
	sup := ready.pluginSup
	identityA := sup.Identity()
	pv, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	loginer, ok := pv.(provider.Loginer)
	if !ok {
		t.Fatalf("provider %T lacks login capability", pv)
	}

	err = loginer.Login(context.Background(), strings.Repeat("x", 5<<20), "")
	if !errors.Is(err, pluginhost.ErrLocalFrameRejected) {
		t.Fatalf("oversized mutation error = %v, want ErrLocalFrameRejected", err)
	}
	if errors.Is(err, ErrUncertainMutation) {
		t.Fatalf("local frame rejection was promoted to ErrUncertainMutation: %v", err)
	}
	if classifyHostLoss(err) != lossNone {
		t.Fatalf("classifyHostLoss(%v) = %v, want lossNone", err, classifyHostLoss(err))
	}
	if sup.StartCount() != 0 {
		t.Fatalf("local frame rejection spawned %d recovery hosts", sup.StartCount())
	}
	if got := sup.Identity(); got.Generation != identityA.Generation {
		t.Fatalf("host identity advanced on a local rejection: %v → %v", identityA, got)
	}
	current, err := ready.Providers.Get("fixture-provider")
	if err != nil {
		t.Fatal(err)
	}
	if current != pv {
		t.Fatal("registry replaced the adapter after a local frame rejection")
	}
	if _, err := current.DetectLive(); err != nil {
		t.Fatalf("host stopped serving after local frame rejection: %v", err)
	}
}

// seedForwardingAccount records an account row for the fixture provider.
func seedForwardingAccount(t *testing.T, a *App, name string, cfg store.ProxyConfig) {
	t.Helper()
	seedFixtureAccount(t, a, name, cfg)
}

// TestProxyGenerationRegression pins the generation contract end to end:
// in-host state stamped for generation A never survives as B's state, exact
// terminated generations drive cleanup, a stale reference cannot kill the
// replacement, and detached/ambiguous rows keep failing closed.
func TestProxyGenerationRegression(t *testing.T) {
	ready, closeReady, events := readyFixtureApp(t)
	defer closeReady()
	ctx := context.Background()
	sup := ready.pluginSup
	identityA := sup.Identity()

	// acct1: a verified in-host listener of generation A.
	seedForwardingAccount(t, ready, "acct1", store.ProxyConfig{Enabled: true})
	res1, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "acct1")
	if err != nil {
		t.Fatal(err)
	}
	healthBody := getOK(t, res1.Endpoint+"/health", "")
	if !strings.Contains(string(healthBody), res1.InstanceID) {
		t.Fatalf("acct1 listener did not echo its instance: %s", healthBody)
	}
	state1, err := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct1")
	if err != nil || state1 == nil {
		t.Fatalf("acct1 state = %+v, %v", state1, err)
	}
	if state1.PluginRuntimeGeneration != identityA.Generation ||
		state1.PluginRuntimePID != identityA.PID {
		t.Fatalf("acct1 row stamped %+v, want generation A %+v",
			state1.PluginRuntimeGeneration, identityA)
	}

	// acct2: cancelled late-binding start → unsettled verdict → the engine
	// force-terminates generation A and keeps a diagnostic record.
	seedForwardingAccount(t, ready, "acct2", store.ProxyConfig{
		Enabled: true,
		Options: map[string]interface{}{"fixture": "bind-cancel-late-overshoot"},
	})
	if err := cancelOvershootStart(t, ready, events, "acct2"); err == nil {
		t.Fatal("cancelled overshoot start must fail")
	}

	waitForCondition(t, "host A terminated", 5*time.Second, func() bool {
		active, running, _ := sup.healthView()
		return active && !running
	})
	// Reconciliation swept A's in-host rows; the diagnostic record stays.
	state1, err = ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct1")
	if err != nil {
		t.Fatal(err)
	}
	if state1 != nil {
		t.Fatalf("in-host generation-A row survived host-loss reconciliation: %+v", state1)
	}
	state2, err := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct2")
	if err != nil || state2 == nil || state2.Token == "" {
		t.Fatalf("unproven diagnostic row must survive: %+v, %v", state2, err)
	}

	// Recover to generation B by an explicit start: host A is already dead,
	// so the request is not-dispatched and recovery may retry once. A
	// dispatched/unsettled start must never take this path.
	seedForwardingAccount(t, ready, "acct3", store.ProxyConfig{Enabled: true})
	res3, err := ready.Proxy.StartProxy(ctx, "fixture-provider", "acct3")
	if err != nil {
		t.Fatalf("recovery start of acct3: %v", err)
	}
	identityB := sup.Identity()
	if identityB.Generation == identityA.Generation {
		t.Fatal("generation did not advance")
	}
	state3, err := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct3")
	if err != nil || state3 == nil {
		t.Fatalf("acct3 state = %+v, %v", state3, err)
	}
	if state3.PluginRuntimeGeneration != identityB.Generation {
		t.Fatalf("acct3 row stamped %q, want generation B %q",
			state3.PluginRuntimeGeneration, identityB.Generation)
	}

	// The STALE reference must not kill B's listener.
	if err := sup.TerminateGeneration(identityA); err == nil {
		t.Fatal("stale generation termination was accepted")
	}
	healthBody = getOK(t, res3.Endpoint+"/health", "")
	if !strings.Contains(string(healthBody), res3.InstanceID) {
		t.Fatalf("B's listener died from a stale termination: health=%s", healthBody)
	}

	// Force-terminating the CURRENT generation cleans exactly B's rows.
	seedForwardingAccount(t, ready, "acct4", store.ProxyConfig{
		Enabled: true,
		Options: map[string]interface{}{"fixture": "bind-cancel-late-overshoot"},
	})
	_ = cancelOvershootStart(t, ready, events, "acct4")
	waitForCondition(t, "host B terminated and reconciled", 8*time.Second, func() bool {
		swept, serr := ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct3")
		return serr == nil && swept == nil
	})
	state3, err = ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct3")
	if err != nil || state3 != nil {
		t.Fatalf("generation-B row survived its own reconciliation: %+v, %v", state3, err)
	}
	state2, err = ready.Store.ReadProxyStateContext(ctx, "fixture-provider", "acct2")
	if err != nil || state2 == nil || state2.Token == "" {
		t.Fatalf("ambiguous pre-B diagnostic row must keep failing closed: %+v, %v", state2, err)
	}
}
