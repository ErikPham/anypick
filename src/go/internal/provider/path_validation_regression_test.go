package provider

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

// singlePasteFile only allows a bare file name, so "a/b.txt" must be rejected
// too; the plain accept case is "b.txt".
func TestPathValidationSinglePasteFile(t *testing.T) {
	if !singlePasteFile("b.txt") {
		t.Fatal("singlePasteFile rejected b.txt")
	}
	if singlePasteFile(pvAccept) {
		t.Errorf("singlePasteFile accepted nested %q", pvAccept)
	}
	for _, c := range pvReject {
		if c.windowsOnly && runtime.GOOS != "windows" {
			continue
		}
		t.Run(c.in, func(t *testing.T) {
			if singlePasteFile(c.in) {
				t.Errorf("singlePasteFile accepted %q", c.in)
			}
		})
	}
}
