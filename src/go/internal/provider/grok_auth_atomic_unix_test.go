//go:build !windows

package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWriteGrokSessionChangesInode is the regression for the truncation bug.
//
// os.WriteFile truncates in place and keeps the inode, so a crash between the
// truncate and the write destroys the live Grok login. Asserting that the inode
// changes is what forces the write through a rename. Revert writeGrokSession to
// os.WriteFile and this test fails.
func TestWriteGrokSessionChangesInode(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeIno := before.Sys().(*syscall.Stat_t).Ino

	s := grokSession{
		storageKey: "grok-session",
		raw: map[string]json.RawMessage{
			"access_token": json.RawMessage(`"rotated"`),
		},
	}
	if err := writeGrokSession(authPath, s); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if ino := after.Sys().(*syscall.Stat_t).Ino; ino == beforeIno {
		t.Fatal("auth.json inode unchanged — the file was truncated in place, not replaced by rename")
	}
}
