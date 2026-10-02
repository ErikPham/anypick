package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"node-free"}, {"other", "root"}} {
		code, _, stderr := captureRun(t, args)
		if code != 2 || !strings.Contains(stderr, "usage:") {
			t.Fatalf("run(%v) = %d stderr=%q", args, code, stderr)
		}
	}
}

func TestRunNodeFree(t *testing.T) {
	root := t.TempDir()
	code, stdout, stderr := captureRun(t, []string{"node-free", root})
	if code != 0 || !strings.Contains(stdout, "Node-free artifact OK") || stderr != "" {
		t.Fatalf("clean = %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if err := os.WriteFile(filepath.Join(root, "node"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = captureRun(t, []string{"node-free", root})
	if code != 1 || stdout != "" || !strings.Contains(stderr, "node") {
		t.Fatalf("violation = %d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func captureRun(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	stdout, stderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	code := run(args)
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = stdout, stderr
	var out, errb bytes.Buffer
	if _, err := io.Copy(&out, rOut); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&errb, rErr); err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}
