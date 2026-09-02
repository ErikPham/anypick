package main

// End-to-end regressions for the P1 fixes: global flags before the command,
// supervisor lifecycle commands, pool enable/disable semantics, and
// project-scoped link/unlink.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/ownership"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/refs"
)

func appReadyForTest() (*app.App, func(), error) {
	return app.Ready(context.Background(), app.Options{SkipMigrate: true})
}

// leakedChildren reports detached children launched from this test's own temp
// binary. Scoping by the test's temp root keeps leak assertions honest: a bare
// `pgrep -f _serve-hub` also matches children left behind by other tests, other
// packages, or earlier runs, so one real leak turns the whole suite red forever
// and no run can say which test leaked.
func leakedChildren(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", regexp.QuoteMeta(t.TempDir())).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil // exit 1 is pgrep's "no process matched"
		}
		// A blocked or missing pgrep must never read as "nothing leaked":
		// silently returning an empty list turns every leak assertion green in
		// a sandbox while the children keep running.
		t.Fatalf("leak detection unavailable (pgrep failed: %v); cannot assert %s", err, t.TempDir())
	}
	return strings.Fields(string(out))
}

// liveOwnedRecords lists detached-child ownership records under ANYPICK_HOME
// that still verify against a live process. Record-based, so it needs no
// external binary and names the exact child that outlived its stop (ADR 0003).
func liveOwnedRecords(t *testing.T, phase string) []string {
	t.Helper()
	root := os.Getenv("ANYPICK_HOME")
	if root == "" {
		return nil
	}
	var found []string
	_ = filepath.WalkDir(filepath.Join(root, "runtime"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "service.pid" {
			return nil
		}
		if rec := ownership.Load(path); rec != nil && rec.VerifyLive() {
			found = append(found, path+" pid="+strconv.Itoa(rec.PID))
		}
		return nil
	})
	if len(found) > 0 {
		t.Logf("%s: %d live ownership record(s) remain: %v", phase, len(found), found)
	}
	return found
}

// waitBudget is the single upper bound for every "wait for it to happen" and
// "prove it did not block" poll in this package.
//
// These tests spawn real detached children and shells out to `go build`, then
// wait for a /health endpoint or for a process to exit. On a loaded machine —
// `make test` runs ~36 packages in parallel, and this package builds the CLI
// inside the test — the old 3-8 second deadlines were not wrong, just tight:
// a slow child looked exactly like a dead one, and the failure named the test
// rather than the timing.
//
// 30s does not weaken the assertions that matter. In dryrun_gate_test the lock
// holder is released by a defer AFTER the assertions, so an operation that
// really does block waits for that release; it cannot pass inside any budget
// the test would still be waiting on.
const waitBudget = 30 * time.Second

// assertNoLeakedChildren waits briefly for a verified stop to take effect, then
// fails with the pids that survived — so a leak always names its own test.
func assertNoLeakedChildren(t *testing.T, phase string) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	for {
		pids := leakedChildren(t)
		if len(pids) == 0 {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("leaked %d detached child(ren) after %s: pids %v under temp root %s; live records: %v",
				len(pids), phase, pids, t.TempDir(), liveOwnedRecords(t, phase))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// isolateEnv points HOME/ANYPICK_HOME at a sandbox for one test.
func isolateEnv(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("ANYPICK_HOME", filepath.Join(base, "data"))
	if err := os.MkdirAll(filepath.Join(base, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestRootGlobalFlagsBeforeCommand(t *testing.T) {
	isolateEnv(t)
	globalJSON = false
	defer func() { globalJSON = false }()
	if code := run([]string{"--json", "providers"}); code != 0 {
		t.Fatalf("--json providers exited %d", code)
	}
}

func TestVersionFlag(t *testing.T) {
	base := isolateEnv(t)
	if code := run([]string{"--version"}); code != 0 {
		t.Fatalf("--version exited %d", code)
	}
	if code := run([]string{"--help"}); code != 0 {
		t.Fatalf("--help exited %d", code)
	}
	if code := run([]string{"--quiet", "help"}); code != 0 {
		t.Fatalf("--quiet help exited %d", code)
	}
	if _, err := os.Stat(filepath.Join(base, "data")); !os.IsNotExist(err) {
		t.Fatalf("identity/help path created the data root: %v", err)
	}
}

func TestSupervisorStatusStop(t *testing.T) {
	isolateEnv(t)
	if code := run([]string{"status"}); code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if code := run([]string{"stop"}); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
}

// TestTrayStatusRoutesToSupervisorStatus pins the P1 fix: TS exposes visible
// `tray status`, so it must print the supervisor status line and never fall
// through into cmdAppRun's desktop snapshot protocol on stdout.
func TestTrayStatusRoutesToSupervisorStatus(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	var out string
	exit := -1
	out, _ = captureBoth(t, func() { exit = run([]string{"tray", "status"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("tray status exited %d, want success", exit)
	}
	if strings.TrimSpace(out) != "not running" {
		t.Fatalf("tray status must print the supervisor status line, got %q", out)
	}
}

func TestPoolDisableActuallyDisables(t *testing.T) {
	isolateEnv(t)
	if code := run([]string{"proxy", "pool", "enable", "deepseek", "--strategy", "round-robin"}); code != 0 {
		t.Fatalf("pool enable exited %d", code)
	}
	pool, err := openPoolForTest(t)
	if err != nil || pool == nil || !pool.Enabled {
		t.Fatalf("pool should be enabled: %+v err=%v", pool, err)
	}
	if pool.Strategy != "round-robin" {
		t.Fatalf("strategy was dropped: %q", pool.Strategy)
	}
	if code := run([]string{"proxy", "pool", "disable", "deepseek"}); code != 0 {
		t.Fatalf("pool disable exited %d", code)
	}
	pool, err = openPoolForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	if pool != nil && pool.Enabled {
		t.Fatalf("pool disable left the pool enabled: %+v", pool)
	}
}

func TestLinkUnlinkAreProjectScoped(t *testing.T) {
	isolateEnv(t)
	// A gateway gives link a resolvable non-account source.
	t.Setenv("ANYPICK_SECRET", "sk-test")
	if code := run([]string{"gateway", "add", "or", "--provider", "openrouter"}); code != 0 {
		t.Fatalf("gateway add exited %d", code)
	}
	if code := run([]string{"link", "claude", "--with", "or"}); code != 0 {
		t.Fatalf("link exited %d", code)
	}
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	global, gerr := a.Bindings.GetGlobalContext(context.Background(), "claude")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if global != nil {
		t.Fatalf("link must not write a global binding: %+v", global)
	}
	cwd, _ := os.Getwd()
	project, perr := a.Bindings.GetProjectContext(context.Background(), cwd, "claude")
	if perr != nil || project == nil {
		t.Fatalf("project binding missing: %+v err=%v", project, perr)
	}

	// Unlink removes the PROJECT binding only.
	if code := run([]string{"unlink", "claude"}); code != 0 {
		t.Fatalf("unlink exited %d", code)
	}
	project, _ = a.Bindings.GetProjectContext(context.Background(), cwd, "claude")
	if project != nil {
		t.Fatalf("unlink left the project binding: %+v", project)
	}
}

var _ = pools.Pool{}

func openPoolForTest(t *testing.T) (*pools.Pool, error) {
	t.Helper()
	a, closeFn, err := appReadyForTest()
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return a.Pools.Get("deepseek")
}

func TestHubActivationEndToEnd(t *testing.T) {
	isolateEnv(t)
	// Detached children must be a real CLI binary, not the test binary.
	bin := filepath.Join(t.TempDir(), "anypick-test-bin")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/anypick")
	cmd.Dir = filepath.Join(currentDir(t), "..", "..")
	out, berr := cmd.CombinedOutput()
	if berr != nil {
		t.Fatalf("build helper binary: %v\n%s", berr, out)
	}
	t.Setenv("ANYPICK_SELF_EXE", bin)
	t.Setenv("ANYPICK_SECRET", "sk-test")
	if code := run([]string{"proxy", "hub", "enable", "test"}); code != 0 {
		t.Fatalf("hub enable exited %d", code)
	}
	if code := run([]string{"add", "deepseek", "--name", "ds", "--kind", "api-key", "--secret", "sk-ds"}); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	if code := run([]string{"proxy", "hub", "source", "test", "deepseek", "ds"}); code != 0 {
		t.Fatalf("hub source exited %d", code)
	}
	if code := run([]string{"use", "claude", "--with", "hub:test"}); code != 0 {
		t.Fatalf("hub activation exited %d — planner/executor must accept enabled hubs", code)
	}
	// The hub child must be serving the planned endpoint.
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cfg, gerr := a.Hub.GetConfig("test")
	if gerr != nil || cfg == nil || cfg.Port == 0 {
		t.Fatalf("hub config missing port: %+v err=%v", cfg, gerr)
	}
	closeFn()
	if !waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port)) {
		t.Fatal("hub child never became healthy")
	}
	stopDetached(hubPIDPath(os.Getenv("ANYPICK_HOME"), "test"))
}

// Pool activation e2e (NATIVE-M1): fixture accounts → pool enable →
// `use claude --with pool:<p>` through the shared port → stop leaves zero
// leaked `_serve-pool` children.
func TestPoolActivationEndToEnd(t *testing.T) {
	isolateEnv(t)
	requireBuiltCLI(t)
	t.Setenv("ANYPICK_SECRET", "sk-test")

	for _, name := range []string{"dsa", "dsb"} {
		if code := run([]string{"add", "deepseek", "--name", name,
			"--kind", "api-key", "--secret", "sk-" + name}); code != 0 {
			t.Fatalf("add %s exited %d", name, code)
		}
	}
	// Deterministic port semantics bind exactly this port (pool.port ??
	// provider default ?? 8080), so pick a free one for hermeticity — a busy
	// planned port must fail loudly, not relocate.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	poolPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if code := run([]string{"proxy", "pool", "enable", "deepseek", "dsa", "dsb",
		"--port", strconv.Itoa(poolPort)}); code != 0 {
		t.Fatalf("pool enable exited %d", code)
	}
	if code := run([]string{"use", "claude", "--with", "pool:deepseek"}); code != 0 {
		t.Fatalf("pool activation exited %d — executor must start the shared pool transport", code)
	}

	// The detached child must be serving the planned endpoint under the
	// primary member's proxy_state record.
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	state, serr := a.Store.ReadProxyState("deepseek", "dsa")
	closeFn()
	if serr != nil || state == nil || state.Endpoint == "" || state.Token == "" {
		t.Fatalf("primary pool state missing: %+v err=%v", state, serr)
	}
	if !waitHTTP(t, state.Endpoint+"/health") {
		t.Fatal("pool child never became healthy")
	}

	// Plan/actual port parity: the client config the activation wrote must
	// point at exactly the served endpoint (no plan-vs-bind divergence).
	rawSettings, rerr := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"))
	if rerr != nil {
		t.Fatalf("claude settings.json missing: %v", rerr)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(rawSettings, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Env["ANTHROPIC_BASE_URL"] != state.Endpoint {
		t.Fatalf("client config endpoint = %q, want served endpoint %q",
			settings.Env["ANTHROPIC_BASE_URL"], state.Endpoint)
	}
	// Credentialed routes are gated even without any reachable upstream:
	// a missing bearer must be rejected by the pool itself, never forwarded.
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(state.Endpoint+"/v1/chat/completions",
		"application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pool request = %d, want 401", resp.StatusCode)
	}

	// Stop via the CLI: the Service verifies ownership via the /health instance
	// echo before signalling; nothing may leak.
	if code := run([]string{"proxy", "pool", "stop", "deepseek"}); code != 0 {
		t.Fatalf("pool stop exited %d", code)
	}
	deadline := time.Now().Add(waitBudget)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, state.Endpoint+"/health", nil)
		resp, err := client.Do(req)
		if err != nil {
			break
		}
		_ = resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	assertNoLeakedChildren(t, "pool stop")

	// A verified stop releases the (provider, "") pool lease (TS
	// releaseLease(providerId, undefined)) — the lease store must be empty.
	a2, closeFn2, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	leases, lerr := a2.Leases.ListPoolLeases("deepseek")
	closeFn2()
	if lerr != nil || len(leases) != 0 {
		t.Fatalf("pool leases survived verified stop: %+v err=%v", leases, lerr)
	}
}

// Stopping a pool that was never configured must report failure (nonzero),
// never print a false success — the old stopDetached-discard path did.
func TestPoolStopReportsFailureForUnknownPool(t *testing.T) {
	isolateEnv(t)
	requireBuiltCLI(t)
	if code := run([]string{"proxy", "pool", "stop", "nosuch"}); code == 0 {
		t.Fatal("stopping a nonexistent pool must exit nonzero")
	}
}

// currentDir returns this test file's package directory.
func currentDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test directory")
	}
	return filepath.Dir(file)
}

func waitHTTP(t *testing.T, url string) bool {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(waitBudget)
	start := time.Now()
	var lastErr error
	var lastStatus int
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			lastStatus = resp.StatusCode
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return true
			}
		} else {
			lastErr = err
			lastStatus = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Name the timing, not just the test: a healthy child that was simply slow
	// must not be reported the same way as one that never listened.
	t.Logf("%s never became healthy in %s (last error: %v, last status: %d)",
		url, time.Since(start).Round(time.Millisecond), lastErr, lastStatus)
	return false
}

// requireBuiltCLI returns a real built CLI binary for detached-child spawns.
func requireBuiltCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "anypick-cli")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/anypick")
	cmd.Dir = filepath.Join(currentDir(t), "..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper binary: %v\n%s", err, out)
	}
	t.Setenv("ANYPICK_SELF_EXE", bin)
	return bin
}

func TestSupervisorStartStaysAlive(t *testing.T) {
	isolateEnv(t)
	requireBuiltCLI(t)

	if code := run([]string{"start"}); code != 0 {
		t.Fatalf("start exited %d", code)
	}
	// The bug this regression pins: the detached supervisor used to exit as
	// soon as its stdin hit EOF, so status flipped to "not running".
	time.Sleep(2 * time.Second)
	globalJSON = true
	defer func() { globalJSON = false }()
	if code := run([]string{"status"}); code != 0 {
		t.Fatal("status failed")
	}
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(supervisorRuntimeDir(a.Root), "service.pid")
	pid := readPID(pidFile)
	closeFn()
	if pid == 0 || !processAlive(pid) {
		t.Fatalf("supervisor died after start (pid=%d)", pid)
	}
	if code := run([]string{"stop"}); code != 0 {
		t.Fatal("stop failed")
	}
	deadline := time.Now().Add(waitBudget)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("supervisor survived stop (pid=%d)", pid)
	}
}

func TestLinkWithoutWithPreservesGlobalSpec(t *testing.T) {
	isolateEnv(t)
	requireBuiltCLI(t)
	t.Setenv("ANYPICK_SECRET", "sk-test")
	if code := run([]string{"add", "deepseek", "--name", "ds", "--kind", "api-key", "--secret", "sk-ds"}); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	// Global binding with an explicit model + transport policy.
	if code := run([]string{"use", "claude", "--with", "deepseek/ds", "--model", "preserved-model", "--transport", "proxy"}); code != 0 {
		t.Fatalf("global use exited %d", code)
	}
	// This activation spawns a persistent detached proxy; register cleanup
	// immediately so neither the normal nor the race suite leaks _serve-proxy
	// children (review finding: orphaned processes across runs).
	t.Cleanup(func() {
		if code := run([]string{"proxy", "stop", "deepseek", "ds"}); code != 0 {
			t.Errorf("cleanup proxy stop exited %d", code)
		}
	})
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	global, gerr := a.Bindings.GetGlobalContext(context.Background(), "claude")
	if gerr != nil || global == nil {
		t.Fatalf("global binding missing: %+v %v", global, gerr)
	}
	wantSpec := global.Spec
	wantGlobalUpdatedAt := global.UpdatedAt
	closeFn()

	// Project link WITHOUT --with must reapply the whole spec.
	if code := run([]string{"link", "claude"}); code != 0 {
		t.Fatalf("project link exited %d", code)
	}
	a, closeFn, err = appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	cwd, _ := os.Getwd()
	project, perr := a.Bindings.GetProjectContext(context.Background(), cwd, "claude")
	if perr != nil || project == nil {
		t.Fatalf("project binding missing: %v", perr)
	}
	if project.Spec.Model.Mode != wantSpec.Model.Mode ||
		project.Spec.Model.ID != wantSpec.Model.ID ||
		project.Spec.TransportPolicy != wantSpec.TransportPolicy ||
		project.Spec.Client != "claude" ||
		fmt.Sprint(project.Spec.Source) != fmt.Sprint(wantSpec.Source) {
		t.Fatalf("spec drifted:\nwant %+v\ngot  %+v", wantSpec, project.Spec)
	}
	// Deep equality: ClientOptions (model roles) and every provenance field
	// must travel with the re-applied spec, not just the model/transport bits.
	if fmt.Sprint(project.Spec.ClientOptions) != fmt.Sprint(wantSpec.ClientOptions) {
		t.Fatalf("clientOptions drifted:\nwant %+v\ngot  %+v", wantSpec.ClientOptions, project.Spec.ClientOptions)
	}
	wantJSON, _ := json.Marshal(wantSpec)
	gotJSON, _ := json.Marshal(project.Spec)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("full spec JSON drifted:\nwant %s\ngot  %s", wantJSON, gotJSON)
	}
	if project.Provenance.Kind != "global_binding_snapshot" {
		t.Fatalf("provenance = %q, want global_binding_snapshot", project.Provenance.Kind)
	}
	if project.Provenance.GlobalBindingUpdatedAt == "" {
		t.Fatal("provenance lost globalBindingUpdatedAt")
	}
	if project.Provenance.GlobalBindingUpdatedAt != wantGlobalUpdatedAt {
		t.Fatalf("globalBindingUpdatedAt = %q, want source updatedAt %q",
			project.Provenance.GlobalBindingUpdatedAt, wantGlobalUpdatedAt)
	}
}

func TestHubActivationAdoptsAndStopsChild(t *testing.T) {
	isolateEnv(t)
	bin := requireBuiltCLI(t)
	t.Setenv("ANYPICK_SECRET", "sk-test")
	if code := run([]string{"proxy", "hub", "enable", "test"}); code != 0 {
		t.Fatalf("hub enable exited %d", code)
	}
	if code := run([]string{"add", "deepseek", "--name", "ds", "--kind", "api-key", "--secret", "sk-ds"}); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	if code := run([]string{"proxy", "hub", "source", "test", "deepseek", "ds"}); code != 0 {
		t.Fatalf("hub source exited %d", code)
	}
	if code := run([]string{"use", "claude", "--with", "hub:test"}); code != 0 {
		t.Fatalf("hub activation exited %d", code)
	}
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	cfg, gerr := a.Hub.GetConfig("test")
	if gerr != nil || cfg == nil || cfg.Port == 0 {
		t.Fatalf("hub config missing port: %+v %v", cfg, gerr)
	}
	closeFn()
	if !waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port)) {
		t.Fatal("hub child never became healthy")
	}

	// Pid-file ownership: `proxy hub stop` must stop the child that
	// activation spawned.
	pidFile := hubPIDPath(os.Getenv("ANYPICK_HOME"), "test")
	pid := readPID(pidFile)
	if pid == 0 {
		t.Fatal("activation did not record a hub service.pid")
	}
	if code := run([]string{"proxy", "hub", "stop", "test"}); code != 0 {
		t.Fatalf("hub stop exited %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(pid) {
		_ = signalPID(pid)
		t.Fatal("hub child survived proxy hub stop — leaked process")
	}
	_ = bin
}

// homeFilesWithToken walks root and returns every file whose bytes contain the
// given secret (best-effort: unreadable files are skipped).
func homeFilesWithToken(root, token string) []string {
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > 8<<20 {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(raw), token) {
			hits = append(hits, path)
		}
		return nil
	})
	return hits
}

// Route lifecycle e2e (NATIVE-M1): enable hub → fixture accounts → `use claude
// --with hub:route` → fresh per-client route token → model-pinned requests hit
// the right source; wrong token 401; detach under the hub scope kills the
// credential; verified stop leaves zero leaked children and clean route rows.
func TestHubRouteLifecycleEndToEnd(t *testing.T) {
	isolateEnv(t)
	requireBuiltCLI(t)
	t.Setenv("ANYPICK_SECRET", "sk-test")
	// Every detached child this test spawns must be gone by the time it ends,
	// not just at the stop assertion below.
	t.Cleanup(func() { assertNoLeakedChildren(t, "hub route lifecycle end") })

	const hubName = "route"
	if code := run([]string{"proxy", "hub", "enable", hubName}); code != 0 {
		t.Fatalf("hub enable exited %d", code)
	}
	for _, name := range []string{"dsa", "dsb"} {
		if code := run([]string{"add", "deepseek", "--name", name,
			"--kind", "api-key", "--secret", "sk-" + name}); code != 0 {
			t.Fatalf("add %s exited %d", name, code)
		}
	}
	if code := run([]string{"proxy", "hub", "source", hubName, "deepseek", "dsa"}); code != 0 {
		t.Fatalf("hub source dsa exited %d", code)
	}
	if code := run([]string{"proxy", "hub", "source", hubName, "deepseek", "dsb"}); code != 0 {
		t.Fatalf("hub source dsb exited %d", code)
	}
	if code := run([]string{"use", "claude", "--with", "hub:" + hubName}); code != 0 {
		t.Fatalf("hub activation exited %d", code)
	}

	readClientEnv := func() (baseURL, token string) {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("claude settings.json missing: %v", err)
		}
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			t.Fatal(err)
		}
		return settings.Env["ANTHROPIC_BASE_URL"], settings.Env["ANTHROPIC_AUTH_TOKEN"]
	}

	endpoint, token1 := readClientEnv()
	if endpoint == "" || token1 == "" {
		t.Fatalf("activation wrote no hub endpoint/token: %q / %q", endpoint, token1)
	}
	if !waitHTTP(t, endpoint+"/health") {
		t.Fatal("hub child never became healthy")
	}

	// Re-attach rotates the per-client credential.
	if code := run([]string{"use", "claude", "--with", "hub:" + hubName}); code != 0 {
		t.Fatalf("second activation exited %d", code)
	}
	endpoint, token2 := readClientEnv()
	if token2 == "" || token2 == token1 {
		t.Fatalf("re-attach did not rotate the route token: %q vs %q", token1, token2)
	}

	// Secret redaction: the plaintext token may exist ONLY in client config
	// (~/.claude/settings.json and the clients/<id>/env.sh mirror — the same
	// boundary class as any proxy credential). Never anywhere else under
	// ANYPICK_HOME (the DB stores only the HMAC), never CLI output.
	statusOut := captureStdout(t, func() {
		if code := run([]string{"proxy", "hub", "status", hubName, "--json"}); code != 0 {
			t.Fatalf("hub status --json exited %d", code)
		}
	})
	if strings.Contains(statusOut, token2) {
		t.Fatal("hub status --json leaked the route token")
	}
	home := os.Getenv("ANYPICK_HOME")
	for _, hit := range homeFilesWithToken(home, token2) {
		if rel, err := filepath.Rel(home, hit); err == nil &&
			strings.HasPrefix(rel, "clients"+string(filepath.Separator)) {
			continue
		}
		t.Fatalf("route token leaked into %s", hit)
	}

	// Make routed requests deterministic + offline: the DB is the authority
	// for snapshots (ADR 0004 — MaterializeSnapshot rebuilds the on-disk cache
	// on every read), so poison each account's stored credential with a
	// DISTINCT unknown endpoint profile. Resolution then fails fast per request
	// with an error naming its account — no network, deterministic source id.
	ctx := context.Background()
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dsa", "dsb"} {
		cred, _ := json.Marshal(map[string]interface{}{
			"version": 1, "kind": "api-key",
			"profile": "stub-endpoint-" + name, "secret": "sk-" + name,
		})
		res, uerr := a.Store.DB.Exec(
			`UPDATE account_snapshot_files SET content = ?
			   WHERE provider = 'deepseek' AND account = ?
			     AND path = 'compatible-credential.json'`, string(cred), name)
		if uerr != nil {
			t.Fatal(uerr)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			t.Fatalf("no stored credential row for deepseek/%s", name)
		}
	}
	closeFn()

	postModelAt := func(model, token string) (*http.Response, string) {
		body, _ := json.Marshal(map[string]string{"model": model})
		req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/chat/completions",
			strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(raw)
	}

	// Model pin "#N" selects the source; each 502 names the endpoint profile
	// the child actually resolved — proving per-model routing through the real
	// binary without touching the network.
	resp, body := postModelAt("#0", token2)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "stub-endpoint-dsa") {
		t.Fatalf("#0 request = %d %s, want 502 naming stub-endpoint-dsa", resp.StatusCode, body)
	}
	resp, body = postModelAt("#1", token2)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "stub-endpoint-dsb") {
		t.Fatalf("#1 request = %d %s, want 502 naming stub-endpoint-dsb", resp.StatusCode, body)
	}

	// Wrong token is rejected by the hub before any routing.
	if resp, _ := postModelAt("#0", "not-the-token"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", resp.StatusCode)
	}

	// Detach runs under the `hub/<name>` mutation scope — never serializeRef —
	// mirroring the executor's critical section.
	a2, closeFn2, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	detachErr := lock.WithMutationLock(ctx, a2.Root, refs.MutationScopeForRef(refs.ProxyHubRef(hubName)), func(c context.Context) error {
		return a2.Hub.DetachRouteContext(c, DefaultHubRouteIDForTest(hubName, "claude"))
	})
	closeFn2()
	if detachErr != nil {
		t.Fatal(detachErr)
	}
	if resp, _ := postModelAt("#0", token2); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token after detach = %d, want 401", resp.StatusCode)
	}

	// Stop the detached child through the health-echo-vetoed CLI stop.
	if code := run([]string{"proxy", "hub", "stop", hubName}); code != 0 {
		t.Fatalf("hub stop exited %d", code)
	}
	assertNoLeakedChildren(t, "hub stop")

	// Clean route-row state: the detached row is gone; config remains.
	a3, closeFn3, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	row, serr := a3.Hub.SnapshotRouteContext(ctx, DefaultHubRouteIDForTest(hubName, "claude"))
	cfg, cerr := a3.Hub.GetConfig(hubName)
	byToken, terr := a3.Hub.RouteByToken(token2)
	closeFn3()
	if serr != nil || row != nil {
		t.Fatalf("detached route row survived: %+v err=%v", row, serr)
	}
	if cerr != nil || cfg == nil {
		t.Fatalf("hub config vanished with the route: %+v err=%v", cfg, cerr)
	}
	if terr != nil || byToken != nil {
		t.Fatalf("detached token still resolves: %+v err=%v", byToken, terr)
	}
}

// DefaultHubRouteIDForTest mirrors the executor's per-client route id shape.
func DefaultHubRouteIDForTest(hubName, client string) string {
	return hubName + "/route-" + client
}
