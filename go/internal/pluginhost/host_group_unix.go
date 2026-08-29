//go:build !windows

package pluginhost

import (
	"os"
	"os/exec"
	"syscall"
)

func setHostGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killHostGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return p.Kill()
}
