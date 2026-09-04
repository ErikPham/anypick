//go:build !windows

package client

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestClaudeSettingsWriteChangesInode is the regression for the truncation bug.
//
// os.WriteFile truncates in place and keeps the inode, so a crash between the
// truncate and the write leaves the user's real ~/.claude/settings.json
// unreadable. The one-time backup this adapter takes is not a fallback: it is
// only written before AnyPick has ever touched the file, so a second Apply
// overwrites the live config with no recoverable copy at all.
//
// Asserting that the inode changes is what forces the write through a rename.
// Revert the atomicfile.Write call in claudeAdapter.Apply to os.WriteFile and
// this test fails.
func TestClaudeSettingsWriteChangesInode(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "anypick")
	// Reset() resolves its checkpoint through anyPickRootFallback(), so
	// ANYPICK_HOME must agree with the root Apply is given.
	t.Setenv("ANYPICK_HOME", root)

	settings := settingsPath(home)
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(settings)
	if err != nil {
		t.Fatal(err)
	}
	beforeIno := before.Sys().(*syscall.Stat_t).Ino

	a := newClaude(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:8787",
		APIKey:       "proxy-secret",
		DefaultModel: "claude-sonnet-4-5",
		AnyPickRoot:  root,
		Home:         home,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after, err := os.Stat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if ino := after.Sys().(*syscall.Stat_t).Ino; ino == beforeIno {
		t.Fatal("settings.json inode unchanged — the file was truncated in place, not replaced by rename")
	}
}
