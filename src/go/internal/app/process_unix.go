//go:build !windows

package app

import "syscall"

func platformProcessAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
