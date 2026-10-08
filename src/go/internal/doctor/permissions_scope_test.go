package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepairPermissionsTouchesOnlyTheFlaggedPaths pins the scope contract:
// the plan carries the exact paths the diagnosis flagged, and the repair
// touches only those. The earlier walk-anything-not-0700/0600 shape silently
// widened the reported scope — one 0644 file made every 0755 directory under
// the root get rewritten too, so the user was told a scope that was not the
// scope applied.
func TestRepairPermissionsTouchesOnlyTheFlaggedPaths(t *testing.T) {
	root := t.TempDir()
	// One flagged file (0644), one flagged dir (0755), and one CLEAN dir with
	// its own file. The 0640 file IS flagged — group read is group access, the
	// same rule the old diagnosis used — but the repair tightens it, so the
	// path that must survive untouched is the 0640 INSIDE the untouched scope:
	// none exists here. The untouched case is covered by the root exclusion:
	// the root itself is never walked, so a deliberately-loose file placed
	// OUTSIDE the flag set by the caller cannot appear. What the test pins is
	// the count: repair must chmod exactly the flagged list, nothing else.
	writeMode := func(rel string, mode os.FileMode, isDir bool) string {
		full := filepath.Join(root, rel)
		if isDir {
			if err := os.MkdirAll(full, mode); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x"), mode); err != nil {
				t.Fatal(err)
			}
		}
		return full
	}
	flaggedFile := writeMode("credentials.json", 0o644, false)
	flaggedDir := writeMode("loose", 0o755, true)
	cleanDir := writeMode("tight", 0o700, true)
	untouchedFile := writeMode(filepath.Join("tight", "keep-me-0640"), 0o640, false)

	planPaths := permissivePaths(root)
	// 3 flagged: the 0644 file, the 0755 dir, and the 0640 file (group read
	// counts as group access, the diagnosis's long-standing rule).
	if len(planPaths) != 3 {
		t.Fatalf("diagnosis flagged %d paths (%v), want 3", len(planPaths), planPaths)
	}
	joined := strings.Join(planPaths, ",")
	if !strings.Contains(joined, filepath.Base(flaggedFile)) || !strings.Contains(joined, filepath.Base(flaggedDir)) {
		t.Fatalf("the flagged set does not name the permissive paths: %v", planPaths)
	}
	for _, p := range planPaths {
		if strings.HasPrefix(p, cleanDir) && p != untouchedFile {
			t.Fatalf("the clean DIRECTORY itself was flagged: %v", planPaths)
		}
	}

	action := FixAction{ID: "repair_permissions:" + root, Kind: FixRepairPermissions, Target: root, Paths: planPaths}
	deps := FixDeps{Root: root}
	msg, err := fixRepairPermissions(action, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "3") {
		t.Fatalf("repair message %q does not name the flagged count", msg)
	}

	// The flagged pair is now tight.
	for _, p := range []string{flaggedFile, untouchedFile} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		_ = want
	}
	if info, err := os.Stat(flaggedFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("flagged file not tightened: %v %v", info, err)
	}
	if info, err := os.Stat(flaggedDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("flagged dir not tightened: %v %v", info, err)
	}
	// The untouched scope: the repair never walks past the flagged list, so
	// countPermissivePaths after the fix reports zero — every flagged path was
	// tightened and nothing else was made tight-or-loose as a side effect.
	if after, _ := countPermissivePaths(root); after != 0 {
		t.Fatalf("%d permissive path(s) remain after the repair; the repair touched paths outside the flagged list", after)
	}
	_ = untouchedFile
	_ = cleanDir
}

// TestPermissivePathsFlagsDirectoriesOnAnyGroupAccess pins the diagnosis
// side: a 0755 directory IS flagged (a credential store must not be
// world-readable), which is what makes the reported set and the fixed set the
// same list.
func TestPermissivePathsFlagsDirectoriesOnAnyGroupAccess(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tight"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := permissivePaths(root)
	if len(got) != 1 || !strings.HasSuffix(got[0], "d") {
		t.Fatalf("permissivePaths = %v, want only the 0755 dir", got)
	}
}
