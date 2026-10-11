package transfer

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Path normalization cases that must be refused the same way on every OS.
var pvAllPlatforms = []string{
	`\x`, `C:\x`, "C:/x", "C:x", `..\x`, `\\server\share\x`, `x\..\..\y`, "x/../..", "/x", "a\x00b",
}

func TestPathValidationTransferImportKeyAllPlatforms(t *testing.T) {
	root := t.TempDir()
	for _, in := range pvAllPlatforms {
		err := ValidateImportFileKey(in)
		if err == nil {
			t.Errorf("ValidateImportFileKey accepted %q", in)
			continue
		}
		if want := fmt.Sprintf("Can't use %q. It points outside AnyPick's folder, so nothing was imported.", in); err.Error() != want {
			t.Errorf("message = %q, want %q", err.Error(), want)
		}
		if got, err := StagedFilePath(root, in); err == nil {
			t.Errorf("StagedFilePath accepted %q -> %q", in, got)
		}
	}
}

func TestPathValidationTransferDecodeFilesAllPlatforms(t *testing.T) {
	for _, in := range pvAllPlatforms {
		files := map[string]json.RawMessage{in: json.RawMessage(`"aGk="`)}
		if out, err := decodeFiles(files); err == nil {
			t.Errorf("decodeFiles accepted %q -> %v", in, out)
		}
	}
}
