package artifact

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The fixtures below are ported from scripts/node-free-artifact-fixtures.mjs.
// They are shared contract: src/macos/scripts/check-node-free-app.sh rejects
// the same shapes, and the two scanners must stay in agreement.

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nodeFreeFixture is one shape the ADR 0020 gate must classify. The slice below
// is the single source of truth for both scanners: adding a shape here drives
// the Go scanner and the Xcode shell scanner, so a shape only one of them
// rejects fails the build. CODE_REVIEW.md §5 — "two implementations of the same
// gate must reject the same shapes" — is why this is a table and not two.
type nodeFreeFixture struct {
	name     string
	wantPass bool
	build    func(t *testing.T, root string)
}

var nodeFreeFixtures = []nodeFreeFixture{
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
		// The shell scanner read only the first 256 bytes with dd, so an
		// interpreter at offset 316 was invisible to Xcode while the Go
		// scanner saw it. Both must reject.
		name:     "node-shebang-past-256-bytes",
		wantPass: false,
		build: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "launch"), "#!"+strings.Repeat(" ", 314)+"node\n")
		},
	},
	{
		name:     "node-shebang-capital",
		wantPass: false,
		build: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "launch"), "#!/usr/bin/env Node\nconsole.log(1)\n")
		},
	},
	{
		name:     "nodejs-shebang-is-not-node",
		wantPass: true,
		build: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "launch"), "#!/usr/bin/env nodejs\nconsole.log(1)\n")
		},
	},
	{
		name:     "homebrew-node-unsuffixed",
		wantPass: false,
		build: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "Info.plist"),
				"helper=/opt/homebrew/opt/node\n")
		},
	},
	{
		name:     "homebrew-node-dotted-version",
		wantPass: false,
		build: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "Info.plist"),
				"helper=/opt/homebrew/opt/node@22.14.0/bin/node\n")
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
		// POSIX [[:space:]] matches a vertical tab; RE2's \s does not. The
		// shell scanner rejected this while the Go scanner accepted it, so
		// the same artifact passed CI and failed Xcode. Both must reject.
		name:     "dev-path-after-vertical-tab",
		wantPass: false,
		build: func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "notes.txt"), "x\v/Users/erik/y\n")
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

func TestNodeFreeFixtures(t *testing.T) {
	for _, tc := range nodeFreeFixtures {
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

// checkNodeFreeShell runs the Xcode-side scanner over root and reports whether
// it passed. The shell scanner had no test executing it at all before this, so
// its two divergences from the Go scanner were free to ship.
func checkNodeFreeShell(t *testing.T, root string) (bool, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the Xcode bundle scanner is a bash script and does not run on Windows")
	}
	script := filepath.Join("..", "..", "..", "macos", "scripts", "check-node-free-app.sh")
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("the shell scanner is missing: %v", err)
	}
	cmd := exec.Command("bash", abs, root)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, string(out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("could not run %s: %v", abs, err)
	}
	return false, string(out)
}

// TestScannersAgreeOnEveryFixture drives both implementations of the ADR 0020
// gate over one fixture set. The rule is CODE_REVIEW.md §5: two implementations
// of the same gate must reject the same shapes. Before this test the shell
// scanner was executed by nothing, and it disagreed with the Go scanner on two
// measured shapes — a Node shebang past byte 256 and a developer path after a
// vertical tab. Each scanner also asserts the expected verdict, so a fixture
// cannot be "agreed" on by two scanners that are both wrong.
func TestScannersAgreeOnEveryFixture(t *testing.T) {
	for _, tc := range nodeFreeFixtures {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)

			goPass := len(InspectNodeFree(root)) == 0
			if goPass != tc.wantPass {
				t.Errorf("go scanner pass = %v, want %v", goPass, tc.wantPass)
			}
			shellPass, output := checkNodeFreeShell(t, root)
			if shellPass != tc.wantPass {
				t.Errorf("shell scanner pass = %v, want %v (output: %s)", shellPass, tc.wantPass, output)
			}
			if goPass != shellPass {
				t.Errorf("scanners disagree: go pass = %v, shell pass = %v (shell output: %s)",
					goPass, shellPass, output)
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
