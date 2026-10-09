package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// captureBoth used to read the pipes only after fn returned, so a command that
// wrote more than the pipe buffer held (16KB on Darwin, 64KB on Linux) blocked
// forever and the package died on its timeout instead of failing a case. The
// output size that triggers it is not exotic: `doctor --fix --dry-run --json`
// printed 82KB once stale temp overlays accumulated in TMPDIR, which is how
// this was found on CI.
//
// These cases write past the buffer deliberately. With the old helper they hang
// until the package timeout; with the draining one they pass in milliseconds.
func TestCaptureBothHandlesOutputLargerThanThePipeBuffer(t *testing.T) {
	isolateEnv(t)

	// Comfortably past the smaller of the two platform buffers.
	const size = 512 << 10

	out, errOut := captureBoth(t, func() {
		fmt.Println(strings.Repeat("x", size))
	})

	if len(out) < size {
		t.Fatalf("captured %d bytes, want at least %d", len(out), size)
	}
	if strings.TrimSpace(out) != strings.Repeat("x", size) {
		t.Fatalf("captured output is not the %d bytes written", size)
	}
	if errOut != "" {
		t.Fatalf("stderr was not empty: %.200q", errOut)
	}
}

func TestCaptureBothInterleavesStdoutAndStderr(t *testing.T) {
	isolateEnv(t)

	// Draining must preserve each stream separately, or a test asserting on
	// stderr reads another test's stdout.
	out, errOut := captureBoth(t, func() {
		fmt.Fprint(os.Stderr, strings.Repeat("e", 128<<10))
		fmt.Fprint(os.Stdout, strings.Repeat("o", 128<<10))
	})

	if !strings.Contains(out, "o") || strings.Contains(out, "e") {
		t.Fatalf("stdout and stderr were mixed: %.80q", out)
	}
	if len(errOut) < 128<<10 || !strings.Contains(errOut, "e") {
		t.Fatalf("stderr captured %d bytes, want at least %d", len(errOut), 128<<10)
	}
}
