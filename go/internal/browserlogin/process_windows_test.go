//go:build windows

package browserlogin

import "testing"

func TestExecutableNamesAddsWindowsExtensions(t *testing.T) {
	t.Setenv("PATHEXT", ".EXE;.CMD")
	names := executableNames("codex")
	if len(names) < 2 || names[0] != "codex.exe" {
		t.Fatalf("executableNames = %#v", names)
	}
}
