package safepath

import (
	"errors"
	"runtime"
	"testing"
)

func TestPathValidationKeyRejects(t *testing.T) {
	for _, in := range []string{
		"/x", `\x`, `C:\x`, "C:/x", "C:x", "c:", "a:b", `\\server\share\x`, "//server/share/x",
		"..", "../x", `..\x`, "x/..", `x\..`, `x\..\..\y`, "x/../..", "a/../b",
	} {
		if err := ValidateKey(in); !errors.Is(err, ErrNotLocal) {
			t.Errorf("ValidateKey(%q) = %v, want ErrNotLocal", in, err)
		}
	}
}

func TestPathValidationKeyEmpty(t *testing.T) {
	if err := ValidateKey(""); !errors.Is(err, ErrEmpty) {
		t.Errorf("ValidateKey(\"\") = %v, want ErrEmpty", err)
	}
}

func TestPathValidationKeyBadNames(t *testing.T) {
	in := []string{
		"a\x00b", "a\x01b", "a\x1fb", "a\x7fb", "a\tb", "dir/a\nb",
	}
	for _, s := range in {
		if err := ValidateKey(s); !errors.Is(err, ErrBadName) {
			t.Errorf("ValidateKey(%q) = %v, want ErrBadName", s, err)
		}
	}
}

func TestPathValidationKeyAccepts(t *testing.T) {
	for _, in := range []string{
		"a", "a/b.txt", `a\b.txt`, "..foo.mjs", "dist/..foo.mjs", "a..b", ".hidden",
		"console.txt", "nullable.json", "COM10", "COM0", "LPT", "auxiliary", "con-fig", "a/./b",
	} {
		if err := ValidateKey(in); err != nil {
			t.Errorf("ValidateKey(%q) rejected: %v", in, err)
		}
	}
}

func TestPathValidationName(t *testing.T) {
	for _, in := range []string{"a/b", `a\b`, ".", "..", "", "C:x", "a\x00b"} {
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

// Reserved device names are refused only on Windows (filepath.IsLocal).
func TestPathValidationReservedNames(t *testing.T) {
	for _, in := range []string{"CON", "con", "Prn.txt", "aux.tar.gz", "dir/NUL.json", `dir\com1`, "LPT9.txt"} {
		err := ValidateKey(in)
		if runtime.GOOS == "windows" && err == nil {
			t.Errorf("ValidateKey(%q) accepted on Windows", in)
		}
		if runtime.GOOS != "windows" && err != nil {
			t.Errorf("ValidateKey(%q) rejected off Windows: %v", in, err)
		}
	}
}

func TestPathValidationImportRefusalMessage(t *testing.T) {
	const noName = "Can't use this file. It contains an entry with no usable name, so nothing was imported."
	for _, in := range []string{"", "a\x00b", "a\x1fb", "a\x7fb"} {
		if got := ImportRefusalMessage(in, ValidateKey(in)); got != noName {
			t.Errorf("ImportRefusalMessage(%q) = %q", in, got)
		}
	}
	const outside = "Can't use \"../x\". It points outside AnyPick's folder, so nothing was imported."
	if got := ImportRefusalMessage("../x", ValidateKey("../x")); got != outside {
		t.Errorf("ImportRefusalMessage(../x) = %q", got)
	}
}
