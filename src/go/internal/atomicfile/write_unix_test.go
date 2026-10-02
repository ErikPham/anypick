//go:build !windows

package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWriteReplacesByRenameKeepsInode is the Unix half of
// TestWriteReplacesByRename: os.WriteFile truncates in place and keeps the
// inode, so only an inode change proves the write went through a rename.
// Windows has no inode to compare, so this assertion lives behind !windows.
func TestWriteReplacesByRenameKeepsInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if inodeOf(t, before) == inodeOf(t, after) {
		t.Fatal("inode unchanged: Write truncated in place instead of renaming")
	}
}

// inodeOf is what makes TestWriteReplacesByRenameKeepsInode meaningful.
// truncates in place and keeps the inode, so only an inode change proves the
// write went through a rename.
func inodeOf(t *testing.T, info os.FileInfo) uint64 {
	t.Helper()
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return st.Ino
}
