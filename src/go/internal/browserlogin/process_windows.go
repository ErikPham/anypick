//go:build windows

package browserlogin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsChildController struct {
	child *exec.Cmd
	job   windows.Handle
}

func executableNames(name string) []string {
	if name == "" || filepath.Ext(name) != "" {
		return []string{name}
	}
	pathExt := os.Getenv("PATHEXT")
	if pathExt == "" {
		pathExt = ".COM;.EXE;.BAT;.CMD"
	}
	extensions := strings.Split(pathExt, ";")
	out := make([]string, 0, len(extensions)+1)
	for _, ext := range extensions {
		if ext = strings.TrimSpace(ext); ext != "" {
			out = append(out, name+strings.ToLower(ext), name+strings.ToUpper(ext))
		}
	}
	return append(out, name)
}

func accessibleExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func startIsolatedChild(child *exec.Cmd) (childController, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	child.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err = child.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(child.Process.Pid),
	)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		_ = windows.CloseHandle(process)
	}
	if err != nil {
		_ = child.Process.Kill()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &windowsChildController{child: child, job: job}, nil
}

func (c *windowsChildController) terminate() {
	// Windows has no SIGTERM contract for arbitrary console children. Closing
	// the private job is the only reliable tree-wide cancellation boundary.
	_ = windows.TerminateJobObject(c.job, 1)
}

func (c *windowsChildController) kill() {
	_ = windows.TerminateJobObject(c.job, 1)
	if c.child.Process != nil {
		_ = c.child.Process.Kill()
	}
}

func (c *windowsChildController) close() { _ = windows.CloseHandle(c.job) }

func processWasSignaled(_ *exec.ExitError) bool { return false }
