//go:build windows

package pluginhost

import (
	"os"
	"os/exec"
	"syscall"
)

func setHostGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func killHostGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
