//go:build windows

package proxy

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

func ownPID() int { return os.Getpid() }

func proxyJobName(pid int) (*uint16, error) {
	return windows.UTF16PtrFromString("AnyPick-PID-" + strconv.Itoa(pid))
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}

func signalPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("no pid")
	}
	name, err := proxyJobName(pid)
	if err != nil {
		return err
	}
	job, jobErr := windows.CreateJobObject(nil, name)
	if jobErr == nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
	// Terminate the recorded main process as well. The caller has already
	// verified its per-instance /health echo, so this cannot target a PID-only
	// guess; the named Job Object additionally takes down descendants.
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if !processAlive(pid) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(process)
	return windows.TerminateProcess(process, 1)
}
