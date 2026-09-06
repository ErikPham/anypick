package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBumpVersion(t *testing.T) {
	cases := []struct{ current, part, want string }{
		{"1.2.3", "major", "2.0.0"},
		{"1.2.3", "minor", "1.3.0"},
		{"1.2.3", "patch", "1.2.4"},
		{"0.0.9", "patch", "0.0.10"},
		{"1.0.0", "major", "2.0.0"},
	}
	for _, c := range cases {
		got, err := bumpVersion(c.current, c.part)
		if err != nil {
			t.Fatalf("bumpVersion(%q, %q): %v", c.current, c.part, err)
		}
		if got != c.want {
			t.Errorf("bumpVersion(%q, %q) = %q, want %q", c.current, c.part, got, c.want)
		}
	}
}

func TestBumpVersionRejectsBadInput(t *testing.T) {
	if _, err := bumpVersion("1.0.0", "epoch"); err == nil {
		t.Error("an unknown part must be rejected")
	}
	if _, err := bumpVersion("not-a-version", "patch"); err == nil {
		t.Error("a non-numeric version must be rejected")
	}
}

func TestPlanBump(t *testing.T) {
	cases := []struct {
		name     string
		messages []string
		want     string
	}{
		{"no commits", nil, ""},
		{"only chores", []string{"chore: tidy up", "docs: fix a typo", "test: add a case"}, ""},
		{"a fix is a patch", []string{"fix(proxy): stop leaking the token"}, "patch"},
		{"perf is a patch", []string{"perf(tui): render once"}, "patch"},
		{"a feature is a minor", []string{"feat(tui): add a form"}, "minor"},
		{"a feature outranks a fix", []string{"fix(core): x", "feat(core): y"}, "minor"},
		{"bang is breaking", []string{"feat(core)!: drop the flag"}, "major"},
		{"footer is breaking", []string{"fix(core): x\n\nBREAKING CHANGE: gone"}, "major"},
		{"breaking outranks feature", []string{"feat: a", "chore!: b"}, "major"},
		{"unparseable subjects are ignored", []string{"merged a branch", "feat: real one"}, "minor"},
		{"scope is allowed", []string{"feat(gemini): write back the credential"}, "minor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := planBump(c.messages); got != c.want {
				t.Errorf("planBump = %q, want %q", got, c.want)
			}
		})
	}
}

// TestSecondarySourcesRoundTrip proves the Cargo and Tauri readers and writers
// agree, and — more importantly — that they do not touch the neighbouring
// version-looking keys that share the file.
func TestSecondarySourcesRoundTrip(t *testing.T) {
	root := t.TempDir()
	mk(t, root, cargoToml, `[package]
name = "anypick-desktop"
version = "1.0.0"
edition = "2021"
rust-version = "1.77.2"

[dependencies]
serde = { version = "1.0", features = ["derive"] }
`)
	mk(t, root, tauriConf, `{
  "$schema": "https://schema.tauri.app/config/2",
  "productName": "AnyPick",
  "version": "1.0.0",
  "identifier": "dev.anypick.desktop",
  "bundle": {
    "version": "9.9.9"
  }
}
`)
	mk(t, root, cargoLock, `[[package]]
name = "anypick-desktop"
version = "1.0.0"
dependencies = [
 "serde 1.0.0",
]

[[package]]
name = "serde"
version = "1.0.0"
source = "registry+https://github.com/rust-lang/crates.io-index"
`)

	for _, src := range secondarySources {
		if src.label == "Xcode MARKETING_VERSION" {
			continue // needs a real pbxproj; covered by the sync check in CI
		}
		got, err := src.read(root)
		if err != nil {
			t.Fatalf("%s read: %v", src.label, err)
		}
		if got != "1.0.0" {
			t.Fatalf("%s read = %q, want 1.0.0", src.label, got)
		}
		if err := src.write(root, "2.3.4"); err != nil {
			t.Fatalf("%s write: %v", src.label, err)
		}
		got, err = src.read(root)
		if err != nil {
			t.Fatalf("%s re-read: %v", src.label, err)
		}
		if got != "2.3.4" {
			t.Errorf("%s after write = %q, want 2.3.4", src.label, got)
		}
	}

	// The dependency pin and the bundle version are not ours to change.
	cargo := read(t, root, cargoToml)
	if want := `serde = { version = "1.0", features = ["derive"] }`; !contains(cargo, want) {
		t.Errorf("Cargo dependency version was rewritten:\n%s", cargo)
	}
	if contains(cargo, `rust-version = "2.3.4"`) {
		t.Errorf("rust-version was rewritten:\n%s", cargo)
	}
	conf := read(t, root, tauriConf)
	if !contains(conf, `"version": "9.9.9"`) {
		t.Errorf("nested bundle version was rewritten:\n%s", conf)
	}
	lock := read(t, root, cargoLock)
	if !contains(lock, "name = \"anypick-desktop\"\nversion = \"2.3.4\"") {
		t.Errorf("lock root version was not bumped:\n%s", lock)
	}
	if !contains(lock, "name = \"serde\"\nversion = \"1.0.0\"") {
		t.Errorf("lock dependency version was rewritten:\n%s", lock)
	}
}

func mk(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
