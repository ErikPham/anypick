// Detached-child helpers run a hidden `_serve-*` subcommand; ownership is the
// structured ADR-0003 record persisted by internal/ownership — a bare PID is
// never proof, so stop verifies live argv identity before signalling.
package main

import (
	"os"
	"path/filepath"
	"time"

	"github.com/outof0/anypick/go/internal/ownership"
)

func runtimeDir(root, kind, name string) string {
	return filepath.Join(root, "runtime", kind, name)
}

func pidPath(root, kind, name string) string {
	return filepath.Join(runtimeDir(root, kind, name), "service.pid")
}

func poolPIDPath(root, providerID string) string { return pidPath(root, "pools", providerID) }
func hubPIDPath(root, hubName string) string     { return pidPath(root, "hub", hubName) }

// spawnDetachedService starts `self <args…>` in its own session and records
// the structured ownership record. Returns the child pid.
func spawnDetachedService(root string, args []string, pidFile string) (int, error) {
	selfExe, err := selfExecutablePath()
	if err != nil {
		return 0, err
	}
	logFile := filepath.Join(filepath.Dir(pidFile), "service.log")
	rec, serr := ownership.Spawn(selfExe, args, pidFile, logFile)
	if serr != nil {
		return 0, serr
	}
	_ = root
	return rec.PID, nil
}

// readPID returns the verified pid behind a TRUSTED ownership record; 0 when
// absent or untrusted (legacy numeric files are removed, never signalled).
func readPID(path string) int {
	rec := ownership.Load(path)
	if rec == nil || !rec.VerifyLive() {
		return 0
	}
	return rec.PID
}

// stopDetached signals the verified session group and waits briefly. Fails
// closed whenever ownership cannot be proven.
func stopDetached(pidFile string) bool {
	return ownership.Stop(pidFile, ownership.StopOptions{Timeout: stopWait})
}

// selfExecutablePath mirrors app.selfExecutable for cmd-level spawns.
func selfExecutablePath() (string, error) {
	if p := os.Getenv("ANYPICK_SELF_EXE"); p != "" {
		return p, nil
	}
	return os.Executable()
}

const stopWait = 3 * time.Second

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return platformProcessAlive(pid)
}
