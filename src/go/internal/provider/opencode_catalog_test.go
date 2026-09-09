package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func writeOpenCodeMirrorAuth(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	raw := "{"
	i := 0
	for service, key := range entries {
		if i > 0 {
			raw += ","
		}
		raw += `"` + service + `":{"type":"api","key":"` + key + `"}`
		i++
	}
	raw += "}"
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenCodeSnapshotCatalogUnionsDefinitionModels(t *testing.T) {
	dir := writeOpenCodeMirrorAuth(t, map[string]string{
		"opencode-go": "sk-go",
		"opencode":    "sk-zen",
		"openai":      "sk-oai-oauth-ish",
	})
	got := OpenCodeSnapshotCatalog(dir)
	want := map[string]bool{}
	for _, id := range append(opencodeZenModels(t), opencodeGoModels(t)...) {
		want[id] = true
	}
	if len(got) != len(want) {
		t.Fatalf("catalog = %v, want %d models", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected model %q (openai entry must not contribute)", id)
		}
	}
}

func TestOpenCodeSnapshotCatalogEmptyWithoutMirror(t *testing.T) {
	if got := OpenCodeSnapshotCatalog(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty dir catalog = %v, want empty", got)
	}
	if got := OpenCodeSnapshotServices(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty dir services = %v, want empty", got)
	}
	if got := OpenCodeMirrorKeys(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty dir keys = %v, want empty", got)
	}
}

func opencodeZenModels(t *testing.T) []string {
	t.Helper()
	for _, def := range BuiltinDefinitions() {
		if def.ID == "opencode-zen" {
			return def.Models
		}
	}
	t.Fatal("opencode-zen definition missing")
	return nil
}

func opencodeGoModels(t *testing.T) []string {
	t.Helper()
	for _, def := range BuiltinDefinitions() {
		if def.ID == "opencode-go" {
			return def.Models
		}
	}
	t.Fatal("opencode-go definition missing")
	return nil
}
