package main

import (
	"context"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// TestTrayWithoutTTYRefusesToStreamProtocol pins the fail-closed behaviour.
// The bare form used to attach the desktop protocol to whatever stdout it was
// given: a pipe or cron job that ran the documented `anypick tray` received
// 29KB of length-prefixed base64 frames from a process that never returned,
// and `tray --json | jq` failed because the envelope was not one JSON value.
func TestTrayWithoutTTYRefusesToStreamProtocol(t *testing.T) {
	restore := resetGlobals()
	defer restore()
	isolateEnv(t)

	code := 0
	_, errOut := captureBoth(t, func() {
		code = cmdTray(context.Background(), nil, []string{"--json"})
	})

	if code != int(anyerr.InvalidUsage) {
		t.Fatalf("tray --json on a non-TTY exited %d, want %d (invalid usage)", code, int(anyerr.InvalidUsage))
	}
	// reportErr prints the human line; the machine code stays on the error's
	// exit path, so assert on the message the surface actually shows.
	if !strings.Contains(errOut, "needs a terminal") {
		t.Fatalf("refusal did not explain the terminal requirement: %q", errOut)
	}
	if !strings.Contains(errOut, "status --json") {
		t.Fatalf("refusal did not point at the snapshot command: %q", errOut)
	}
}

// TestTrayHelpPrintsUsageAndExits covers the other half: --help is help, not a
// protocol attachment attempt, and it succeeds rather than exiting 2.
func TestTrayHelpPrintsUsageAndExits(t *testing.T) {
	restore := resetGlobals()
	defer restore()
	isolateEnv(t)

	var out string
	code := 0
	out, _ = captureBoth(t, func() {
		code = cmdTray(context.Background(), nil, []string{"--help"})
	})

	if code != int(anyerr.Success) {
		t.Fatalf("tray --help exited %d, want 0", code)
	}
	if !strings.Contains(out, "tray start|stop|status") {
		t.Fatalf("help did not document the subcommands: %q", out)
	}
}
