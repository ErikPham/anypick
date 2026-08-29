//go:build !windows

package browserlogin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// fakeLauncher writes an executable shell script and returns its path.
func fakeLauncher(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "launcher.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var ae *anyerr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("want AnyPickError, got %v", err)
	}
	return ae.Code
}

func TestResolveBinaryPrefersExtraPathsThenPath(t *testing.T) {
	dir := t.TempDir()
	name := "anypick-fake-bin-" + time.Now().Format("150405.000")
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveBinary([]string{name}, []string{path}); got != path {
		t.Fatalf("extraPaths candidate missed: %q", got)
	}
	t.Setenv("PATH", dir)
	if got := ResolveBinary([]string{name}, nil); got != path {
		t.Fatalf("PATH candidate missed: %q", got)
	}
	if got := ResolveBinary([]string{"definitely-missing-bin"}, nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestAvailabilityReportsMissingLauncher(t *testing.T) {
	m := Availability(Method{ID: "m", Label: "M", Provenance: "official"}, Command{
		Names: []string{"definitely-missing-launcher"},
	})
	if m.Available || m.UnavailableReason == "" {
		t.Fatalf("availability = %+v", m)
	}
	existing := fakeLauncher(t, "exit 0")
	m = Availability(Method{ID: "m", Label: "M"}, Command{Names: []string{existing}})
	if !m.Available {
		t.Fatalf("existing launcher marked unavailable: %+v", m)
	}
}

func TestRunSuccessAndNoOutputForwarding(t *testing.T) {
	script := fakeLauncher(t, `echo "https://oauth.example/secret?code=one-time"; exit 0`)
	err := Run(context.Background(), Command{Names: []string{script}}, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunSetsHomeEnv(t *testing.T) {
	home := t.TempDir()
	script := fakeLauncher(t, `[ "$HOME" = "`+home+`" ] && [ ! -t 1 ] || exit 9`)
	if err := Run(context.Background(), Command{Names: []string{script}}, home); err != nil {
		t.Fatalf("Run with HOME check: %v", err)
	}
}

func TestRunFailureCodes(t *testing.T) {
	failed := fakeLauncher(t, "exit 3")
	err := Run(context.Background(), Command{Names: []string{failed}}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_FAILED" {
		t.Fatalf("nonzero exit: %v", err)
	}
	interrupted := fakeLauncher(t, "kill -TERM $$")
	err = Run(context.Background(), Command{Names: []string{interrupted}}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_FAILED" {
		t.Fatalf("signal exit: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "nothing-here")
	err = Run(context.Background(), Command{Names: []string{missing}}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_UNAVAILABLE" {
		t.Fatalf("missing binary must be unavailable, got %v", err)
	}
}

func TestRunCancellation(t *testing.T) {
	script := fakeLauncher(t, "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := Run(ctx, Command{Names: []string{script}}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_CANCELLED" {
		t.Fatalf("cancel: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestRunPreCancelledContext(t *testing.T) {
	script := fakeLauncher(t, "exit 0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, Command{Names: []string{script}}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_CANCELLED" {
		t.Fatalf("pre-cancelled: %v", err)
	}
}

func TestRunTimeout(t *testing.T) {
	script := fakeLauncher(t, "sleep 30")
	err := Run(context.Background(), Command{Names: []string{script}, Timeout: 200 * time.Millisecond}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_TIMEOUT" {
		t.Fatalf("timeout: %v", err)
	}
}

func TestRunKillsProcessGroup(t *testing.T) {
	// The script backgrounds a child; a cancelled run must not leave it alive.
	dir := t.TempDir()
	marker := filepath.Join(dir, "still-running")
	script := fakeLauncher(t, "( sleep 30; touch "+marker+" ) &\nwait")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	if err := Run(ctx, Command{Names: []string{script}}, dir); err == nil {
		t.Fatal("want cancellation error")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("backgrounded helper survived cancellation (process-group not killed)")
	}
	_ = syscall.Kill(0, 0) // sanity: syscall import used on all platforms
}

func TestRunForcesKillWhenLauncherIgnoresTerm(t *testing.T) {
	// A launcher that traps SIGTERM must not hold the caller forever: after
	// the grace period the process group is SIGKILLed and Run returns.
	script := fakeLauncher(t, "trap '' TERM\nsleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := Run(ctx, Command{Names: []string{script}}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_CANCELLED" {
		t.Fatalf("cancel: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > TerminationGrace+5*time.Second {
		t.Fatalf("forced termination took too long: %v", elapsed)
	}
}

func TestRunTimeoutForcesKillWhenLauncherIgnoresTerm(t *testing.T) {
	script := fakeLauncher(t, "trap '' TERM\nsleep 30")
	start := time.Now()
	err := Run(context.Background(), Command{
		Names: []string{script}, Timeout: 150 * time.Millisecond,
	}, t.TempDir())
	if codeOf(t, err) != "PROVIDER_LOGIN_TIMEOUT" {
		t.Fatalf("timeout: %v", err)
	}
	if elapsed := time.Since(start); elapsed > TerminationGrace+5*time.Second {
		t.Fatalf("forced termination took too long: %v", elapsed)
	}
}
