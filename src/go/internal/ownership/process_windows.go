//go:build windows

package ownership

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errNoOwnedJob means neither the stamped job nor a verified legacy job
// contains the recorded PID. Closing the handle of a job this lookup created
// is enough: an empty job dies with its last handle, and TerminateJobObject
// on it would not reach the child.
var errNoOwnedJob = errors.New("no ownership job contains the recorded process")

type windowsDetachedProcess struct {
	job windows.Handle
}

func openNamedJob(name string) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateJobObject(nil, ptr)
}

func openJob(pid int, start uint64) (windows.Handle, error) {
	return openNamedJob(jobName(pid, start))
}

// openOwnedJob returns the job that contains rec.PID. The stamped name is
// preferred. The PID-only name is opened only when the live process matches
// the record and the stamped job does not hold the PID — a legacy child from
// before the start stamp (ADR 0003). CreateJobObject of a missing name
// creates an empty job; that handle is closed here and never terminated.
// A truncated process list is an error, not a miss.
func openOwnedJob(rec *Record) (windows.Handle, error) {
	if rec == nil || rec.PID <= 0 || rec.ProcessStart == 0 {
		return 0, errNoOwnedJob
	}
	stamped, err := openNamedJob(jobName(rec.PID, rec.ProcessStart))
	if err != nil {
		return 0, err
	}
	stampedHolds, qerr := jobContainsPID(stamped, rec.PID)
	if qerr != nil {
		_ = windows.CloseHandle(stamped)
		return 0, qerr
	}
	verified := verifyLiveRecord(rec)
	var legacy windows.Handle
	legacyHolds := false
	if verified && !stampedHolds {
		legacy, err = openNamedJob(legacyJobName(rec.PID))
		if err != nil {
			_ = windows.CloseHandle(stamped)
			return 0, err
		}
		legacyHolds, qerr = jobContainsPID(legacy, rec.PID)
		if qerr != nil {
			_ = windows.CloseHandle(stamped)
			_ = windows.CloseHandle(legacy)
			return 0, qerr
		}
	}
	name, ok := selectStopJob(rec.PID, rec.ProcessStart, verified, stampedHolds, legacyHolds)
	if !ok {
		_ = windows.CloseHandle(stamped)
		if legacy != 0 {
			_ = windows.CloseHandle(legacy)
		}
		return 0, errNoOwnedJob
	}
	if name == jobName(rec.PID, rec.ProcessStart) {
		if legacy != 0 {
			_ = windows.CloseHandle(legacy)
		}
		return stamped, nil
	}
	_ = windows.CloseHandle(stamped)
	return legacy, nil
}

// jobPIDListHeader is the fixed prefix of JOBOBJECT_BASIC_PROCESS_ID_LIST.
// x/sys exposes the information class but not the struct. ULONG_PTR ids follow
// at offset 8 on both amd64 and 386.
type jobPIDListHeader struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
}

func jobContainsPID(job windows.Handle, pid int) (bool, error) {
	const capIDs = 4096
	buf := make([]byte, int(unsafe.Sizeof(jobPIDListHeader{}))+capIDs*int(unsafe.Sizeof(uintptr(0))))
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&buf[0])),
		uint32(len(buf)),
		nil,
	)
	if err != nil {
		return false, err
	}
	header := (*jobPIDListHeader)(unsafe.Pointer(&buf[0]))
	assigned := header.NumberOfAssignedProcesses
	listed := header.NumberOfProcessIdsInList
	if assigned > capIDs || listed > capIDs || listed > assigned {
		return false, fmt.Errorf("job process list truncated: assigned %d listed %d", assigned, listed)
	}
	if listed == 0 {
		return false, nil
	}
	ids := unsafe.Slice((*uintptr)(unsafe.Add(unsafe.Pointer(&buf[0]), unsafe.Sizeof(jobPIDListHeader{}))), listed)
	want := uintptr(pid)
	for _, id := range ids {
		if id == want {
			return true, nil
		}
	}
	return false, nil
}

func jobHasActiveProcesses(job windows.Handle) (bool, error) {
	var info jobObjectBasicAccountingInformation
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return false, err
	}
	return info.ActiveProcesses > 0, nil
}

func terminateJob(job windows.Handle) error {
	if err := windows.TerminateJobObject(job, 1); err != nil &&
		!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return err
	}
	return nil
}

func terminateProcess(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.TerminateProcess(handle, 1)
}

// stampedJobWithDescendants opens the stamped job when the leader PID is gone
// but children this build assigned are still in it. A missing name creates an
// empty job; that one is closed without being terminated.
func stampedJobWithDescendants(rec *Record) (windows.Handle, bool) {
	if rec == nil || rec.PID <= 0 || rec.ProcessStart == 0 {
		return 0, false
	}
	job, err := openNamedJob(jobName(rec.PID, rec.ProcessStart))
	if err != nil {
		return 0, false
	}
	alive, qerr := jobHasActiveProcesses(job)
	if qerr != nil || !alive {
		_ = windows.CloseHandle(job)
		return 0, false
	}
	return job, true
}

// createFreshJob opens the job named by this process's pid and start stamp.
// That stamp is the collision key (ADR 0003). GetLastError is not consulted:
// CreateJobObject does not promise to clear it on a fresh create, so a stale
// ERROR_ALREADY_EXISTS on the thread would refuse every Windows spawn.
func createFreshJob(pid int, start uint64) (windows.Handle, error) {
	return openJob(pid, start)
}

func startDetached(cmd *exec.Cmd, _ string) (detachedProcess, error) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_, start, ierr := captureProcessIdentity(cmd.Process.Pid)
	if ierr != nil {
		_ = cmd.Process.Kill()
		return nil, ierr
	}
	job, err := createFreshJob(cmd.Process.Pid, start)
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

// jobObjectBasicAccountingInformation mirrors the Win32 struct of the same
// name. x/sys/windows exposes the information class but not the struct, and the
// only field Stop reads is ActiveProcesses.
type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// groupAlive reports whether any process of the child's job object is still
// running. Windows assigns the child and everything it spawns to one job, so
// this is the counterpart of the Unix session-group check: it is what lets Stop
// claim the whole tree is down rather than only its leader.
//
// An unreadable job reports "still alive": the caller then escalates to
// TerminateJobObject and a short bounded wait, which is the safe direction for
// a stop.
func groupAlive(rec *Record) bool {
	if rec == nil || rec.PID <= 0 {
		return false
	}
	// Opening a missing stamped name creates an empty job. ActiveProcesses on
	// that empty job is 0, which used to make Stop report the tree gone while
	// a legacy PID-only job still held the child (ADR 0003).
	job, err := openOwnedJob(rec)
	if err != nil {
		if stamped, ok := stampedJobWithDescendants(rec); ok {
			_ = windows.CloseHandle(stamped)
			return true
		}
		if verifyLiveRecord(rec) {
			return processAlive(rec.PID)
		}
		return false
	}
	defer windows.CloseHandle(job)
	alive, qerr := jobHasActiveProcesses(job)
	if qerr != nil {
		return true
	}
	return alive
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
	job, err := openOwnedJob(rec)
	if err != nil {
		if stamped, ok := stampedJobWithDescendants(rec); ok {
			defer windows.CloseHandle(stamped)
			return terminateJob(stamped)
		}
		if verifyLiveRecord(rec) {
			return terminateProcess(rec.PID)
		}
		if errors.Is(err, errNoOwnedJob) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(job)
	return terminateJob(job)
}
