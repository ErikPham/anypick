//go:build !windows && !darwin

package ownership

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Linux and other procfs unixes read identity straight from /proc: no external
// binary, so a restricted or sandboxed environment cannot make Stop fail
// closed and leak the child it should reap (ADR 0003).

func liveArgv(pid int) ([]string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out, nil
}

// processStartStamp reports field 22 of /proc/<pid>/stat: the time the process
// started after system boot, in clock ticks. Units only need to be internally
// consistent — a recycled pid reports a different value.
func processStartStamp(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseProcStartStamp(pid, string(raw))
}

// parseProcStartStamp reads overall field 22 (starttime) from a /proc/<pid>/stat
// line. After the closing ')' of comm, fields[0] is state (overall field 3), so
// starttime is fields[19]. Index 20 is vsize and grows after spawn — reading it
// made VerifyLive fail for every real Go child on Linux (supervisor orphans).
func parseProcStartStamp(pid int, line string) (uint64, error) {
	idx := strings.LastIndex(line, ")")
	if idx < 0 || idx+2 >= len(line) {
		return 0, fmt.Errorf("/proc/%d/stat: unexpected shape", pid)
	}
	fields := strings.Fields(line[idx+2:])
	const starttimeField = 19 // overall field 22; fields[0] == state (overall 3)
	if len(fields) <= starttimeField {
		return 0, fmt.Errorf("/proc/%d/stat: too few fields", pid)
	}
	return strconv.ParseUint(fields[starttimeField], 10, 64)
}

func captureProcessIdentity(pid int) (string, uint64, error) {
	stamp, err := processStartStamp(pid)
	if err != nil {
		return "", 0, err
	}
	execPath, lerr := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if lerr != nil {
		return "", stamp, nil
	}
	return execPath, stamp, nil
}
