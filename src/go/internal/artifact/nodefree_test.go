package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below are ported from scripts/node-free-artifact-fixtures.mjs.
// They are shared contract: macos/scripts/check-node-free-app.sh rejects the
// same shapes, and the two scanners must stay in agreement.

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNodeFreeFixtures(t *testing.T) {
	tests := []struct {
		name     string
		wantPass bool
		build    func(t *testing.T, root string)
	}{
		{
			name:     "clean-helper-bundle",
			wantPass: true,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "Contents", "Helpers", "anypick-core"), "elf")
				writeFile(t, filepath.Join(root, "Contents", "Info.plist"), "<plist></plist>\n")
			},
		},
		{
			name:     "stale-plugin-host",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t,
					filepath.Join(root, "Contents", "Resources", "plugin-host", "plugin-host.mjs"),
					"export {}\n")
			},
		},
		{
			name:     "forbidden-filename-node",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "Helpers", "node"), "#!/bin/sh\n")
			},
		},
		{
			name:     "forbidden-filename-npm",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "Helpers", "npm"), "#!/bin/sh\n")
			},
		},
		{
			name:     "forbidden-filename-pnpm",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "Helpers", "pnpm"), "#!/bin/sh\n")
			},
		},
		{
			name:     "forbidden-filename-tsx",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "Helpers", "tsx"), "#!/bin/sh\n")
			},
		},
		{
			name:     "node-shebang",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "launch"), "#!/usr/bin/env node\nconsole.log(1)\n")
			},
		},
		{
			name:     "dev-path-early",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "Info.plist"),
					"helper=/Users/erik/workspace/lab/js/hotpug/go\n")
			},
		},
		{
			// The scan deliberately covers the whole file, not a 4 KiB prefix:
			// a path past that offset must still be caught.
			name:     "dev-path-after-4096",
			wantPass: false,
			build: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "notes.txt"),
					strings.Repeat("x", 4100)+"\n/Users/erik/workspace/lab/js/hotpug\n")
			},
		},
		{
			name:     "external-symlink",
			wantPass: false,
			build: func(t *testing.T, root string) {
				if err := os.Symlink("/bin/sh", filepath.Join(root, "escape")); err != nil {
					t.Skipf("symlink unsupported here: %v", err)
				}
			},
		},
		{
			name:     "dangling-symlink",
			wantPass: false,
			build: func(t *testing.T, root string) {
				if err := os.Symlink(filepath.Join(root, "missing-target"),
					filepath.Join(root, "dangling")); err != nil {
					t.Skipf("symlink unsupported here: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)

			violations := InspectNodeFree(root)
			gotPass := len(violations) == 0
			if gotPass != tc.wantPass {
				t.Fatalf("pass = %v, want %v (violations: %v)", gotPass, tc.wantPass, violations)
			}
		})
	}
}

func TestContentScanSkipsFilesOverTheBound(t *testing.T) {
	// The bound is a performance guard, so a leaked path beyond it is out of
	// contract rather than a false negative. Pinning the behaviour stops
	// someone "fixing" it by raising the bound and quietly changing the gate.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "big.bin"),
		strings.Repeat("x", MaxTextBytes+1)+"\n/Users/erik/workspace\n")

	if violations := InspectNodeFree(root); len(violations) != 0 {
		t.Fatalf("file over MaxTextBytes must be exempt from the content scan, got %v", violations)
	}
}

func TestMissingRootIsReported(t *testing.T) {
	violations := InspectNodeFree(filepath.Join(t.TempDir(), "nope"))
	if len(violations) == 0 {
		t.Fatal("a missing artifact root must be reported, not silently pass")
	}
	if !strings.Contains(violations[0].Message, "is not a directory") {
		t.Errorf("message = %q, want it to say the root is not a directory", violations[0].Message)
	}
}

func TestBinaryFilesAreNotScannedAsText(t *testing.T) {
	// A NUL byte means the file is not text; scanning it would produce
	// nonsense matches.
	root := t.TempDir()
	content := append([]byte("binary\x00payload\n"), []byte("/Users/erik/x")...)
	if err := os.WriteFile(filepath.Join(root, "blob"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if violations := InspectNodeFree(root); len(violations) != 0 {
		t.Fatalf("binary file must be exempt from the text scan, got %v", violations)
	}
}
