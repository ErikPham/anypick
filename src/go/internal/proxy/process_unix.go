//go:build !windows

package proxy

import (
	"fmt"
	"os"
	"syscall"
)

func ownPID() int { return os.Getpid() }

// processAlive reports whether a pid exists. On unix, signal 0 probes without
// delivering a signal.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

// signalPID sends SIGTERM to a detached proxy child.
func signalPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("no pid")
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
