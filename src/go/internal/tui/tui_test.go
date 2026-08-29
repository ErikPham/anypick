package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestIsTTYRejectsCharacterDevicesThatAreNotTerminals(t *testing.T) {
	// /dev/null is a character device, so the obvious
	// `Mode()&os.ModeCharDevice != 0` check calls it a terminal. The TUI would
	// then start, read no keystrokes, and hang — which is what cron, CI and
	// `anypick tui < /dev/null` all do. Regression guard.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	if isTTY(devNull) {
		t.Error("isTTY(/dev/null) = true, want false: it is a char device but not a terminal")
	}
}

func TestIsTTYRejectsPipesAndRegularFiles(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if isTTY(reader) {
		t.Error("isTTY(pipe) = true, want false")
	}

	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTTY(f) {
		t.Error("isTTY(regular file) = true, want false")
	}
}

func TestRunFailsClosedWithoutATerminal(t *testing.T) {
	// A nil *app.App is fine on this path: the TTY guard runs before any
	// service is touched, and that ordering is what makes it fail closed.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	if err := Run(context.Background(), nil, devNull, devNull); !errors.Is(err, ErrNoTTY) {
		t.Fatalf("Run() error = %v, want ErrNoTTY", err)
	}
}

func TestErrNoTTYNamesTheCommandsThatWorkWithoutOne(t *testing.T) {
	// A piped invocation needs somewhere to go, so the error must name the
	// non-interactive commands instead of only refusing.
	for _, command := range []string{"anypick list", "anypick use", "anypick status"} {
		if !strings.Contains(ErrNoTTY.Error(), command) {
			t.Errorf("ErrNoTTY = %q, want it to mention %q", ErrNoTTY.Error(), command)
		}
	}
}
