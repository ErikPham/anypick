package lock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets this binary re-exec itself as a lock-holding child process.
// In-process contention never reaches flock (pathMutex serializes first), so
// cross-process behaviour needs a real second process.
func TestMain(m *testing.M) {
	if os.Getenv("ANYPICK_LOCK_HELPER") == "hold" {
		path := os.Getenv("ANYPICK_LOCK_HELPER_PATH")
		holdMS, _ := strconv.Atoi(os.Getenv("ANYPICK_LOCK_HELPER_HOLD_MS"))
		l, err := AcquireFileLock(path, 0)
		if err != nil {
			os.Exit(3)
		}
		if ready := os.Getenv("ANYPICK_LOCK_HELPER_READY"); ready != "" {
			_ = os.WriteFile(ready, []byte("ready"), 0o600)
		}
		time.Sleep(time.Duration(holdMS) * time.Millisecond)
		_ = l.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startHolder(t *testing.T, lockPath, ready string, hold time.Duration) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^$")
	cmd.Env = append(os.Environ(),
		"ANYPICK_LOCK_HELPER=hold",
		"ANYPICK_LOCK_HELPER_PATH="+lockPath,
		"ANYPICK_LOCK_HELPER_HOLD_MS="+strconv.Itoa(int(hold.Milliseconds())),
		"ANYPICK_LOCK_HELPER_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func awaitReady(t *testing.T, ready string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAcquireFileLockContentionAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "mutation.lock")
	ready := filepath.Join(dir, "ready")
	startHolder(t, lockPath, ready, 400*time.Millisecond)
	awaitReady(t, ready)

	// Try-lock (timeout 0) fails immediately while another process holds it.
	start := time.Now()
	if _, err := AcquireFileLock(lockPath, 0); err == nil ||
		!strings.Contains(err.Error(), "Timed out") {
		t.Fatalf("try-lock should fail fast, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("try-lock waited %v — must be immediate", elapsed)
	}

	// A short timeout still expires while the lock is held.
	if _, err := AcquireFileLock(lockPath, 80*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "Timed out") {
		t.Fatalf("short timeout should expire, got %v", err)
	}

	// Waiting long enough acquires once the holder releases.
	l, err := AcquireFileLock(lockPath, 5*time.Second)
	if err != nil {
		t.Fatalf("waited acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release the same path can be acquired again in-process.
	l2, err := AcquireFileLock(lockPath, 0)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireFileLockDirCreationFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// MkdirAll cannot create a directory below a regular file.
	if _, err := AcquireFileLock(filepath.Join(blocker, "sub", "x.lock"), 0); err == nil {
		t.Fatal("want FS_PATH error for unusable lock dir")
	}
}

func TestReleaseNilIsNoop(t *testing.T) {
	var l *FileLock
	if err := l.Release(); err != nil {
		t.Fatalf("nil release: %v", err)
	}
}
