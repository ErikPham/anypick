//go:build !windows

package proxy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/store"
)

// TestReconcileKeepsPausedDetachedProxyAcrossWholeWindow is the timing-vs-
// death regression: a detached proxy SIGSTOPped longer than the ENTIRE
// verification window misses every health probe exactly like a corpse, but
// its recorded process identity is still alive. Darkness alone must never
// drop its state and leases; after SIGCONT the very same instance serves
// again, still tracked.
//
// SIGSTOP/SIGCONT have no Windows equivalent, so this lives behind !windows.
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
	got, serr := svc.Store.ReadProxyState(ctx, "pluginprov", "paused")
	if serr != nil || got == nil || got.Token != "tok-paused" || got.InstanceID != instanceID {
		t.Fatalf("SIGSTOPed detached proxy forgotten across the whole window: %+v, %v", got, serr)
	}
	if after, _ := svc.Jobs.GetContext(context.Background(), lease.LeaseID); after == nil {
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

// TestScopedDoubleDetachDelayedBind pins the detached-grandchild orphan class
// from the re-review: a recorded launcher whose pid is now dead may have
// detached a grandchild that binds only later. The prover must NOT treat the
// dead launcher pid as proof the whole process tree is gone — it must keep the
// diagnostic scoped record and report unproven, so the still-unbound
// grandchild is not lost as a permanent untracked listener.
func TestScopedDoubleDetachDelayedBind(t *testing.T) {
	instanceID := "99999999-9999-4999-8999-999999999999"
	grandchildPIDFile := filepath.Join(t.TempDir(), "grandchild.pid")

	// Launcher helper detaches a grandchild (a long sleep) then exits, leaving
	// the grandchild alive behind it — exactly the process generation the
	// recorded pid alone cannot account for.
	launcher := exec.Command(os.Args[0], "-test.run=TestScopedDoubleDetachHelper$")
	launcher.Env = append(os.Environ(),
		"GO_TEST_SCOPED_GRANDCHILD_PID_FILE="+grandchildPIDFile)
	if err := launcher.Start(); err != nil {
		t.Skipf("cannot spawn launcher helper: %v", err)
	}
	launcherPID := launcher.Process.Pid
	_, _ = launcher.Process.Wait() // launcher exits after detaching its grandchild
	t.Cleanup(func() {
		if data, rerr := os.ReadFile(grandchildPIDFile); rerr == nil {
			if gp, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil {
				_ = syscall.Kill(gp, syscall.SIGKILL)
			}
		}
	})

	port, err := PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	req := scopedTestRequest(t, port)
	// The record names the (now dead) launcher pid; the endpoint is silent
	// because the detached grandchild has not bound yet.
	writeScopedRecord(t, req, instanceID, launcherPID,
		plannedEndpointURL(req), time.Now(), nil)

	if err := killScopedProxyChild(req, plannedEndpointURL(req), ""); err == nil {
		t.Fatal("dead launcher with a surviving detached grandchild must not be proven dead")
	}
	if _, err := os.Stat(filepath.Join(req.RuntimeDir, "proxy.pid")); errors.Is(err, os.ErrNotExist) {
		t.Fatal("diagnostic record was cleared while a detached descendant may still bind")
	}
}

// TestScopedDoubleDetachHelper detaches a grandchild (a long sleep, new session)
// and exits, leaving the grandchild alive behind the dead launcher pid recorded
// by TestScopedDoubleDetachDelayedBind.
func TestScopedDoubleDetachHelper(t *testing.T) {
	grandchild := exec.Command("sleep", "120")
	grandchild.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := grandchild.Start(); err != nil {
		t.Skipf("cannot spawn grandchild: %v", err)
	}
	if f := os.Getenv("GO_TEST_SCOPED_GRANDCHILD_PID_FILE"); f != "" {
		if werr := os.WriteFile(f, []byte(strconv.Itoa(grandchild.Process.Pid)), 0o600); werr != nil {
			t.Fatalf("write grandchild pid: %v", werr)
		}
	}
	_ = grandchild.Process.Release() // detach so it outlives this helper
	// Return normally: the test binary exits on its own (testing.Main calls
	// os.Exit, which is allowed), and the detached grandchild — now its own
	// session leader — survives that exit.
}
