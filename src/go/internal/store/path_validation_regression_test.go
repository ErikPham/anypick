package store

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

func TestPathValidationStoreImportKey(t *testing.T) {
	root := t.TempDir()
	if err := validateImportFileKey(pvAccept); err != nil {
		t.Fatalf("validateImportFileKey rejected %q: %v", pvAccept, err)
	}
	if _, err := stagedFilePath(root, pvAccept); err != nil {
		t.Fatalf("stagedFilePath rejected %q: %v", pvAccept, err)
	}
	for _, c := range pvReject {
		if c.windowsOnly && runtime.GOOS != "windows" {
			continue
		}
		t.Run(c.in, func(t *testing.T) {
			if err := validateImportFileKey(c.in); err == nil {
				t.Errorf("validateImportFileKey accepted %q", c.in)
			}
			if got, err := stagedFilePath(root, c.in); err == nil {
				t.Errorf("stagedFilePath accepted %q -> %q", c.in, got)
			}
		})
	}
}
