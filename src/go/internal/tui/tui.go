package tui

import (
	"context"
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
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
		// The original error still carries suggestions. A fresh errors.New
		// drops them before reportErr can print the recovery commands.
		if model.statusCause != nil {
			return model.statusCause
		}
		return errors.New(model.status)
	}
	return nil
}

// isTTY reports whether f is a terminal that can deliver keystrokes.
//
// It must NOT be `Mode()&os.ModeCharDevice != 0`: /dev/null is a character
// device too, so that test calls it a terminal and the TUI then starts, reads
// nothing, and hangs — exactly what happens under cron, CI and `< /dev/null`.
// isatty asks the terminal driver (TCGETS/isatty) instead.
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}
