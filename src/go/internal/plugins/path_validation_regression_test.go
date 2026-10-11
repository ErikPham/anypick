package plugins

// Regression tests for Windows path validation (QA). Windows-only inputs are
// skipped elsewhere because backslash and "C:" are ordinary filename bytes there.

import (
	"path/filepath"
	"runtime"
	"testing"
)

var _ = filepath.Join

type pvCase struct {
	in          string
	windowsOnly bool
}

var pvReject = []pvCase{
	{`\x`, true},
	{`C:\x`, true},
	{`C:x`, true},
	{`..\x`, true},
	{`\\server\share\x`, true},
	{`x\..\..\y`, true},
	{`x/../..`, false},
}

const pvAccept = "a/b.txt"

func TestPathValidationResolveEntry(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveEntry(root, pvAccept); err != nil {
		t.Fatalf("ResolveEntry rejected %q: %v", pvAccept, err)
	}
	for _, c := range pvReject {
		if c.windowsOnly && runtime.GOOS != "windows" {
			continue
		}
		t.Run(c.in, func(t *testing.T) {
			if got, err := ResolveEntry(root, c.in); err == nil {
				t.Errorf("ResolveEntry accepted %q -> %q", c.in, got)
			}
		})
	}
}
