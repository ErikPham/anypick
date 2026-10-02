package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exit codes are the contract CI branches on: 0 clean, 1 violations,
// 2 usage. A check that reports a problem with a zero exit is the failure mode
// this command replaced.
func TestRunExitCodes(t *testing.T) {
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "VERSION"), []byte("1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no arguments", nil, 2},
		{"unknown subcommand", []string{"everything"}, 2},
		{"too many arguments", []string{"paths", "one", "two"}, 2},
		{"root without the sentinel", []string{"paths", t.TempDir()}, 1},
		// The clean directory has none of the files the checks parse, so every
		// check reports a missing reference: a gate must not pass by finding
		// nothing to look at.
		{"nothing to inspect", []string{"paths", clean}, 1},
		{"protocol with nothing to inspect", []string{"protocol", clean}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := run(tc.args, &out, &errOut); got != tc.want {
				t.Fatalf("run(%v) = %d, want %d\nstdout:\n%s\nstderr:\n%s",
					tc.args, got, tc.want, out.String(), errOut.String())
			}
		})
	}
}

func TestRunPrintsFindingsAndAFailureSummary(t *testing.T) {
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "VERSION"), []byte("1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"paths", empty}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "check FAILED") {
		t.Fatalf("stderr carries no summary: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "  FAIL ") {
		t.Fatalf("stdout carries no findings: %q", out.String())
	}
}
