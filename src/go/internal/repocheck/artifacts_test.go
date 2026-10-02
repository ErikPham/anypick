package repocheck

import (
	"strings"
	"testing"
)

// TestArtifactFindings covers the tracking rule for every shape a tracked file
// can take. The exemption is decided by magic bytes, never by extension: a
// Mach-O renamed to icon.png is exactly how a build artifact would get back in.
func TestArtifactFindings(t *testing.T) {
	pngHead := append(append([]byte{}, pngMagic...), 0x00)
	icnsHead := append(append([]byte{}, icnsMagic...), 0x00)
	machoHead := []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01}

	blobs := map[string]struct {
		size int64
		head []byte
	}{
		":src/macos/Resources/AppIcon.icns": {64, icnsHead},
		":assets/icon.png":                  {128, pngHead},
		":src/go/blob.bin":                  {4096, machoHead},
		":assets/icon.png.bin":              {128, pngHead},
		":assets/fake.png":                  {4096, machoHead},
		":assets/empty.bin":                 {0, nil},
		":assets/unreadable.bin":            {-1, nil},
		":assets/truncated.icns":            {2, []byte{'i'}},
	}
	blob := func(spec string) (int64, []byte) {
		entry, ok := blobs[spec]
		if !ok {
			t.Fatalf("unexpected blob read: %s", spec)
		}
		return entry.size, entry.head
	}

	tracked := []string{
		"src/go/main.go",                   // text
		"src/macos/Resources/AppIcon.icns", // verified icon
		"assets/icon.png",                  // verified image
		"src/go/blob.bin",                  // tracked binary
		"assets/icon.png.bin",              // image bytes, unexplained name
		"assets/fake.png",                  // extension without magic
		"assets/empty.bin",                 // tracked placeholder
		"assets/unreadable.bin",            // git could not report it
		"assets/truncated.icns",            // magic cut short
	}
	text := []string{"src/go/main.go"}

	found := artifactFindings(tracked, text, blob)
	byDetail := map[string]Finding{}
	for _, finding := range found {
		byDetail[finding.Detail] = finding
	}

	wantViolations := []string{
		"src/go/blob.bin is not text and not a verified image asset",
		"assets/icon.png.bin is not text and not a verified image asset",
		"assets/fake.png is not text and not a verified image asset",
		"assets/unreadable.bin could not be inspected",
		"assets/truncated.icns is not text and not a verified image asset",
	}
	for _, want := range wantViolations {
		finding, ok := byDetail[want]
		if !ok {
			t.Errorf("missing finding %q (got %v)", want, keys(byDetail))
			continue
		}
		if len(finding.Advice) == 0 {
			t.Errorf("%q carries no advice", want)
		}
	}
	if len(found) != len(wantViolations) {
		t.Errorf("findings = %d, want %d: %v", len(found), len(wantViolations), keys(byDetail))
	}
	// Excused shapes must not appear at all. Compare the path prefix exactly:
	// "assets/icon.png" is excused while "assets/icon.png.bin" is not.
	for _, excused := range []string{
		"src/macos/Resources/AppIcon.icns",
		"assets/icon.png",
		"assets/empty.bin",
	} {
		for detail := range byDetail {
			if strings.HasPrefix(detail, excused+" ") {
				t.Errorf("%q must be excused, got %q", excused, detail)
			}
		}
	}
}

func keys(m map[string]Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPassedAndFindings(t *testing.T) {
	clean := []Section{{Name: "a", Checked: []string{"x"}}}
	if !Passed(clean) || len(Findings(clean)) != 0 {
		t.Fatal("a section with no findings must pass")
	}
	broken := []Section{
		{Name: "a", Checked: []string{"x"}},
		{Name: "b", Findings: []Finding{fail("boom")}},
	}
	if Passed(broken) {
		t.Fatal("a section with a finding must fail the run")
	}
	if got := Findings(broken); len(got) != 1 || got[0].Detail != "boom" {
		t.Fatalf("Findings = %v", got)
	}
}

func TestRootWalksUpToTheSentinel(t *testing.T) {
	fixture := newFixtureRepo(t)
	nested := fixture.root + "/src/go/internal/buildinfo"
	root, err := Root(nested)
	if err != nil {
		t.Fatalf("Root(%s): %v", nested, err)
	}
	if root != fixture.root {
		t.Fatalf("Root = %q, want %q", root, fixture.root)
	}
	if _, err := Root(t.TempDir()); err == nil {
		t.Fatal("a directory without the sentinel must not resolve to a root")
	}
}
