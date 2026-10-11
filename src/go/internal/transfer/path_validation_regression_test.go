package transfer

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

func TestPathValidationTransferImportKey(t *testing.T) {
	root := t.TempDir()
	if err := ValidateImportFileKey(pvAccept); err != nil {
		t.Fatalf("ValidateImportFileKey rejected %q: %v", pvAccept, err)
	}
	if _, err := StagedFilePath(root, pvAccept); err != nil {
		t.Fatalf("StagedFilePath rejected %q: %v", pvAccept, err)
	}
	for _, c := range pvReject {
		if c.windowsOnly && runtime.GOOS != "windows" {
			continue
		}
		t.Run(c.in, func(t *testing.T) {
			if err := ValidateImportFileKey(c.in); err == nil {
				t.Errorf("ValidateImportFileKey accepted %q", c.in)
			}
			if got, err := StagedFilePath(root, c.in); err == nil {
				t.Errorf("StagedFilePath accepted %q -> %q", c.in, got)
			}
		})
	}
}
