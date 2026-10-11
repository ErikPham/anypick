package safepath

import "testing"

func TestPathValidationKeyRejects(t *testing.T) {
	for _, in := range []string{
		"", "/x", `\x`, `C:\x`, "C:/x", "C:x", "c:", `\\server\share\x`, "//server/share/x",
		"..", "../x", `..\x`, "x/..", `x\..`, `x\..\..\y`, "x/../..", "a/../b", "a\x00b",
	} {
		if err := ValidateKey(in); err == nil {
			t.Errorf("ValidateKey(%q) accepted", in)
		}
	}
}

func TestPathValidationKeyAccepts(t *testing.T) {
	for _, in := range []string{"a", "a/b.txt", `a\b.txt`, "..foo.mjs", "dist/..foo.mjs", "a..b", ".hidden", "a/./b"} {
		if err := ValidateKey(in); err != nil {
			t.Errorf("ValidateKey(%q) rejected: %v", in, err)
		}
	}
}

func TestPathValidationName(t *testing.T) {
	for _, in := range []string{"a/b", `a\b`, ".", "..", "", "C:x"} {
		if ValidateName(in) == nil {
			t.Errorf("ValidateName(%q) accepted", in)
		}
	}
	for _, in := range []string{"auth.json", "..foo.json", ".credentials.json"} {
		if err := ValidateName(in); err != nil {
			t.Errorf("ValidateName(%q) rejected: %v", in, err)
		}
	}
}
