//go:build !windows

package ownership

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

type unixDetachedProcess struct {
	pid int
}

func startDetached(cmd *exec.Cmd, _ string) (detachedProcess, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &unixDetachedProcess{pid: cmd.Process.Pid}, nil
}

func (p *unixDetachedProcess) terminate() error { return syscall.Kill(-p.pid, syscall.SIGTERM) }
func (p *unixDetachedProcess) close() error     { return nil }

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// groupAlive reports whether any process of the child's session group is still
// running. The child is a session leader (Setsid), so its pid is its group id
// and `kill(-pid, 0)` reaches every member it spawned — including grandchildren
// that inherited the group. ESRCH means the group is gone, which is the only
// answer that lets Stop claim the tree is down.
//
// A process that daemonizes itself with its own setsid leaves this group and
// cannot be reached this way; that is what the ownership record and the reap
// path are for.
func groupAlive(rec *Record) bool {
	if rec == nil || rec.PID <= 0 {
		return false
	}
	err := syscall.Kill(-rec.PID, 0)
	if err == nil {
		return true
	}
	// EPERM means a member exists but is not ours to signal — still alive.
	return errors.Is(err, syscall.EPERM)
}

// verifyLiveRecord proves current ownership: the pid must be alive, its live
// command line must contain every recorded argument token, and — when the
// record carries a start stamp — the live process must have started at exactly
// that time. A recycled pid running identical arguments cannot pass the start
// check, and a blocked external binary can no longer make verification fail
// (ADR 0003 §3).
func verifyLiveRecord(rec *Record) bool {
	tokens, err := liveArgv(rec.PID)
	if err != nil {
		return false
	}
	set := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		set[token] = true
	}
	for _, want := range rec.Command {
		if !set[want] {
			return false
		}
	}
	if rec.ProcessStart != 0 {
		start, serr := processStartStamp(rec.PID)
		if serr != nil || start != rec.ProcessStart {
			return false
		}
	}
	return true
}

// spawnIdentity captures identity after the child has exec'd. Reading it
// immediately after fork is racy: a forked-but-not-yet-exec'd process still
// reports the parent's argv and executable, which would poison the record and
// make every later stop fail closed.
func spawnIdentity(pid int, args []string) (string, uint64, error) {
	var (
		executable string
		stamp      uint64
		lastErr    error
	)
	deadline := time.Now().Add(2 * time.Second)
	for attempt := 0; ; attempt++ {
		executable, stamp, lastErr = captureProcessIdentity(pid)
		if lastErr == nil {
			if tokens, aerr := liveArgv(pid); aerr == nil && containsAll(tokens, args) {
				return executable, stamp, nil
			}
		}
		if !processAlive(pid) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Best effort: a child that already exited leaves nothing to verify, and a
	// platform that cannot report identity degrades to argv-only verification
	// rather than refusing to start the service.
	return executable, stamp, lastErr
}

func containsAll(tokens, want []string) bool {
	set := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		set[token] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func terminateRecord(rec *Record, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	return syscall.Kill(-rec.PID, signal)
}
