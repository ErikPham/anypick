package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSHA256SUMS(t *testing.T) {
	const sample = `
# comment
d3ba775502491fd4a490ad323a1979953a2bc6eba15684c97e305d412578c3e2  AnyPick-macos-universal.zip
04a411890364b135eddc3ba7671e394cb939d3110c0901a1205227c5df51b36a  AnyPick_1.1.0_x64-setup.exe
`
	sums, err := ParseSHA256SUMS(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if sums["AnyPick-macos-universal.zip"] != "d3ba775502491fd4a490ad323a1979953a2bc6eba15684c97e305d412578c3e2" {
		t.Fatalf("zip hash: %q", sums["AnyPick-macos-universal.zip"])
	}
	if len(sums) != 2 {
		t.Fatalf("len=%d", len(sums))
	}
}

func TestVerifyFileSHA256MatchAndMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob")
	content := []byte("any-pick-test-bytes")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	want := HashBytes(content)
	if err := VerifyFileSHA256(path, want); err != nil {
		t.Fatalf("match: %v", err)
	}
	if err := VerifyFileSHA256(path, strings.Repeat("ab", 32)); err == nil {
		t.Fatal("mismatch should fail")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want mismatch wording, got %v", err)
	}
}

func TestApplyChecksums(t *testing.T) {
	assets := []Asset{{Name: "a.zip"}, {Name: "b.zip"}}
	sums := map[string]string{"a.zip": "aa"}
	out := ApplyChecksums(assets, sums)
	if out[0].SHA256 != "aa" || out[1].SHA256 != "" {
		t.Fatalf("%+v", out)
	}
}
