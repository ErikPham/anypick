package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootOverridePrecedence(t *testing.T) {
	t.Setenv("ANYPICK_HOME", "/tmp/from-env")
	got := Root("/tmp/explicit")
	if got != "/tmp/explicit" {
		t.Fatalf("Root override=%q want /tmp/explicit", got)
	}
	// ~/ expansion should use actual HOME, not env.
	home, _ := os.UserHomeDir()
	got = Root("~/mydir")
	want := filepath.Join(home, "mydir")
	if got != want {
		t.Fatalf("Root(~/mydir)=%q want %q", got, want)
	}
}

func TestRootEnvFallback(t *testing.T) {
	t.Setenv("ANYPICK_HOME", "/tmp/env-root")
	got := Root("")
	if got != "/tmp/env-root" {
		t.Fatalf("Root env=%q want /tmp/env-root", got)
	}
	// ANYPICK_HOME with ~/ should expand.
	t.Setenv("ANYPICK_HOME", "~/envtilde")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "envtilde")
	got = Root("")
	if got != want {
		t.Fatalf("Root ANYPICK_HOME=~/envtilde => %q want %q", got, want)
	}
}

func TestRootDefault(t *testing.T) {
	t.Setenv("ANYPICK_HOME", "")
	// No override, no env => ~/.anypick
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".anypick")
	got := Root("")
	if got != want {
		t.Fatalf("Root default=%q want %q", got, want)
	}
}

func TestPathBuilders(t *testing.T) {
	root := "/tmp/root"
	if got := ProviderDir(root, "claude"); got != filepath.Join(root, "providers", "claude") {
		t.Fatalf("ProviderDir %q", got)
	}
	if got := AccountDir(root, "claude", "work"); !strings.HasSuffix(got, filepath.Join("providers", "claude", "accounts", "work")) {
		t.Fatalf("AccountDir %q", got)
	}
	if got := AccountSnapshotDir(root, "claude", "work"); !strings.HasSuffix(got, filepath.Join("accounts", "work", "snapshot")) {
		t.Fatalf("AccountSnapshotDir %q", got)
	}
	if got := SnapshotStagingDir(root); got != filepath.Join(root, "staging", "snapshots") {
		t.Fatalf("SnapshotStagingDir %q", got)
	}
	if got := DBPath(root); got != filepath.Join(root, "anypick.db") {
		t.Fatalf("DBPath %q", got)
	}
	if got := LocksDir(root); got != filepath.Join(root, "locks") {
		t.Fatalf("LocksDir %q", got)
	}
}
