//go:build !windows

package proxy

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestWaitForProxyExitBlocksUntilTheProcessIsGone(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	}()
	start := time.Now()
	if err := waitForProxyExit(pid, func() bool { return true }, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("returned in %s while the process was still alive", elapsed)
	}
	if processAlive(pid) {
		t.Fatal("process still alive")
	}
}
