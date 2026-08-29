package tui

import (
	"context"
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/outof0/anypick/go/internal/app"
)

// ErrNoTTY reports that the interactive TUI was started without a terminal on
// stdin. Invariant #6: never prompt without a TTY — a caller that pipes input
// gets an actionable error instead of a program that reads from a pipe and
// appears to hang.
var ErrNoTTY = errors.New("the interactive TUI requires a terminal: run `anypick list`, `anypick use` or `anypick status` instead")

// Run starts the terminal UI and blocks until the user quits.
//
// stdin/stdout are parameters rather than process globals so the TTY guard and
// the program can be driven by tests.
func Run(ctx context.Context, a *app.App, stdin *os.File, stdout *os.File) error {
	if !isTTY(stdin) {
		return ErrNoTTY
	}
	m := New(ctx, a)
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithInput(stdin),
		tea.WithOutput(stdout),
		tea.WithAltScreen(),
	)
	final, err := p.Run()
	if err != nil {
		return err
	}
	if model, ok := final.(*Model); ok && model.statusErr && model.status != "" {
		// Surface a failed action as a non-zero exit so `anypick tui` can be
		// used in scripts that care, without printing anything extra to stdout.
		return errors.New(model.status)
	}
	return nil
}

// isTTY reports whether f is a character device. Used on stdin: the TUI reads
// keys, so a pipe or a file is not a usable input.
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
