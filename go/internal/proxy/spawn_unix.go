//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package proxy

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/outof0/anypick/go/internal/store"
)

// ExecSpawner returns a Spawner that launches selfExe as a detached
// (setsid) child serving the account's proxy. The child reads its token,
// port, and instance id from proxy_state — nothing secret crosses argv.
func ExecSpawner(selfExe string) Spawner {
	return func(providerID, accountName string, st store.ProxyRuntimeState, logPath string) (int, error) {
		cmd := exec.Command(selfExe, "_serve-proxy", providerID, accountName)
		cmd.Env = append(os.Environ(), "ANYPICK_PROXY_CHILD=1")
		if logPath != "" {
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err == nil {
				cmd.Stdout = f
				cmd.Stderr = f
				defer f.Close()
			}
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		pid := cmd.Process.Pid
		go func() { _ = cmd.Wait() }() // reap when the child eventually exits
		return pid, nil
	}
}
