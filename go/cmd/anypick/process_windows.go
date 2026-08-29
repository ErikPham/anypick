//go:build windows

package main

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

func commandJobName(pid int) (*uint16, error) {
	return windows.UTF16PtrFromString("AnyPick-PID-" + strconv.Itoa(pid))
}

func platformProcessAlive(pid int) bool {
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

func startPlatformProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	name, err := commandJobName(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	job, err := windows.CreateJobObject(nil, name)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	_ = windows.CloseHandle(job)
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	return nil
}

func terminatePlatformPID(pid int) error {
	name, err := commandJobName(pid)
	if err == nil {
		if job, jobErr := windows.CreateJobObject(nil, name); jobErr == nil {
			_ = windows.TerminateJobObject(job, 1)
			_ = windows.CloseHandle(job)
		}
	}
	process, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if openErr != nil {
		if !platformProcessAlive(pid) {
			return nil
		}
		return openErr
	}
	defer windows.CloseHandle(process)
	return windows.TerminateProcess(process, 1)
}
