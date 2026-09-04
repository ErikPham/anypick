//go:build !windows

package atomicfile

import (
	"os"
	"syscall"
	"testing"
)

// inodeOf is what makes TestWriteReplacesByRename meaningful: os.WriteFile
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
