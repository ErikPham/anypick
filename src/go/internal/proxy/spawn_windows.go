//go:build windows

package proxy

import (
	"os"
	"os/exec"

	"github.com/outof0/anypick/go/internal/store"
	"golang.org/x/sys/windows"
)

func ExecSpawner(selfExe string) Spawner {
	return func(providerID, accountName string, _ store.ProxyRuntimeState, logPath string) (int, error) {
		cmd := exec.Command(selfExe, "_serve-proxy", providerID, accountName)
		cmd.Env = append(os.Environ(), "ANYPICK_PROXY_CHILD=1")
		output, err := openProxyChildOutput(logPath)
		if err != nil {
			return 0, err
		}
		defer output.Close()
		cmd.Stdout, cmd.Stderr = output, output
		cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		name, err := proxyJobName(cmd.Process.Pid)
		if err != nil {
			_ = cmd.Process.Kill()
			return 0, err
		}
		job, err := windows.CreateJobObject(nil, name)
		if err != nil {
			_ = cmd.Process.Kill()
			return 0, err
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
			return 0, err
		}
		pid := cmd.Process.Pid
		go func() {
			_ = cmd.Wait()
			_ = windows.CloseHandle(job)
		}()
		return pid, nil
	}
}
