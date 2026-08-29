//go:build darwin

package ownership

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Process identity on darwin is read through sysctl instead of shelling out to
// /bin/ps. An external binary is unavailable inside a sandboxed or hardened
// runtime, and a blocked `ps` used to make every ownership check fail closed —
// so Stop() refused to signal anything and leaked the very child it existed to
// reap (ADR 0003).

// procargs2 layout: int32 argc, executable path (NUL-terminated), NUL padding,
// then argc NUL-terminated argv strings, then the environment.
func readProcArgs(pid int) (execPath string, argv []string, err error) {
	buf, rerr := unix.SysctlRaw("kern.procargs2", pid)
	if rerr != nil {
		return "", nil, rerr
	}
	if len(buf) < 4 {
		return "", nil, fmt.Errorf("kern.procargs2: short buffer for pid %d", pid)
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	idx := bytes.IndexByte(rest, 0)
	if idx < 0 {
		return "", nil, fmt.Errorf("kern.procargs2: unterminated exec path for pid %d", pid)
	}
	execPath = string(rest[:idx])
	rest = rest[idx:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	out := make([]string, 0, argc)
	for i := 0; i < argc && len(rest) > 0; i++ {
		n := bytes.IndexByte(rest, 0)
		if n < 0 {
			out = append(out, string(rest))
			break
		}
		out = append(out, string(rest[:n]))
		rest = rest[n+1:]
	}
	return execPath, out, nil
}

func liveArgv(pid int) ([]string, error) {
	_, argv, err := readProcArgs(pid)
	return argv, err
}

// processStartStamp reports process creation time in microseconds since the
// epoch — the PID-reuse guard: argv tokens alone can match a recycled pid
// running the same command.
func processStartStamp(pid int) (uint64, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	start := k.Proc.P_starttime
	return uint64(start.Sec)*uint64(time.Second/time.Microsecond) + uint64(start.Usec), nil
}

func captureProcessIdentity(pid int) (string, uint64, error) {
	execPath, _, err := readProcArgs(pid)
	if err != nil {
		return "", 0, err
	}
	stamp, serr := processStartStamp(pid)
	if serr != nil {
		return execPath, 0, serr
	}
	return execPath, stamp, nil
}
