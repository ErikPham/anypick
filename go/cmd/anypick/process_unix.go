//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func platformProcessAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func startPlatformProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func terminatePlatformPID(pid int) error { return syscall.Kill(-pid, syscall.SIGTERM) }
