//go:build windows

package ownership

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

type windowsDetachedProcess struct {
	job windows.Handle
}

func jobName(pid int) string { return "AnyPick-PID-" + strconv.Itoa(pid) }

func openJob(pid int) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(jobName(pid))
	if err != nil {
		return 0, err
	}
	return windows.CreateJobObject(nil, name)
}

func startDetached(cmd *exec.Cmd, _ string) (detachedProcess, error) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	job, err := openJob(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
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
	if err != nil {
		_ = cmd.Process.Kill()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &windowsDetachedProcess{job: job}, nil
}

func (p *windowsDetachedProcess) terminate() error { return windows.TerminateJobObject(p.job, 1) }
func (p *windowsDetachedProcess) close() error     { return windows.CloseHandle(p.job) }

func captureProcessIdentity(pid int) (string, uint64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", 0, err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err = windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", 0, err
	}
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", 0, err
	}
	stamp := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return windows.UTF16ToString(buffer[:size]), stamp, nil
}

// spawnIdentity has no fork/exec race on Windows: CreateProcess starts the new
// image directly, so identity is valid as soon as the handle opens.
func spawnIdentity(pid int, _ []string) (string, uint64, error) {
	return captureProcessIdentity(pid)
}

func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}

func verifyLiveRecord(rec *Record) bool {
	if rec.Executable == "" || rec.ProcessStart == 0 {
		return false
	}
	executable, started, err := captureProcessIdentity(rec.PID)
	return err == nil && started == rec.ProcessStart && strings.EqualFold(executable, rec.Executable)
}

func terminateRecord(rec *Record, _ bool) error {
	job, err := openJob(rec.PID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	if err = windows.TerminateJobObject(job, 1); err != nil &&
		!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return err
	}
	return nil
}
