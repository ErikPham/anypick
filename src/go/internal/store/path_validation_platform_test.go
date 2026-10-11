package store

import (
	"fmt"
	"testing"
)

// Path normalization cases that must be refused the same way on every OS.
var pvAllPlatforms = []string{
	`\x`, `C:\x`, "C:/x", "C:x", `..\x`, `\\server\share\x`, `x\..\..\y`, "x/../..", "/x", "a\x00b",
}

func TestPathValidationStoreImportKeyAllPlatforms(t *testing.T) {
	root := t.TempDir()
	for _, in := range pvAllPlatforms {
		err := validateImportFileKey(in)
		if err == nil {
			t.Errorf("validateImportFileKey accepted %q", in)
			continue
		}
		if want := fmt.Sprintf("Can't use %q. It points outside AnyPick's folder, so nothing was imported.", in); err.Error() != want {
			t.Errorf("message = %q, want %q", err.Error(), want)
		}
		if got, err := stagedFilePath(root, in); err == nil {
			t.Errorf("stagedFilePath accepted %q -> %q", in, got)
		}
	}
}
