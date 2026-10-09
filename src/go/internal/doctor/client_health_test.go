package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/db"
)

// A client can be wired up and still be unable to start: a config file its own
// parser rejects keeps its managed block, so the presence check passes while
// every request fails. DiagnoseClient reported "installed" for exactly that,
// which is the one word that hides it. The rule is exercised through the shipped
// code path, not a copy of it.
func TestInstalledWithIssuesIsNotReportedHealthy(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// The real corruption: a managed-block marker tail left at the document root.
	corrupt := "-native-profile\n\n# BEGIN anypick-managed\nmodel = \"m\"\n" +
		"model_provider = \"anypick\"\n# END anypick-managed\n"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := DiagnoseClient(context.Background(), doctorDeps(t, home), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) == 0 {
		t.Fatal("no findings")
	}
	got := rep.Findings[0]
	if got.Check != "client" || got.Status != "warn" {
		t.Fatalf("client finding = %#v, want check client status warn", got)
	}
	if got.Detail == "OpenAI Codex installed" || !strings.Contains(got.Detail, "line 1") {
		t.Fatalf("client detail hid the broken config: %q", got.Detail)
	}
}

// A sound install must stay silent, or the Apps board becomes noise.
func TestInstalledWithIssuesStaysQuietWhenHealthy(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# BEGIN anypick-managed\nmodel = \"m\"\n"+
		"model_provider = \"anypick\"\n# END anypick-managed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := DiagnoseClient(context.Background(), doctorDeps(t, home), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) == 0 {
		t.Fatal("no findings")
	}
	got := rep.Findings[0]
	if got.Check != "client" || got.Status != "ok" || got.Detail != "OpenAI Codex installed" {
		t.Fatalf("client finding = %#v, want ok %q", got, "OpenAI Codex installed")
	}
}

func doctorDeps(t *testing.T, home string) Deps {
	t.Helper()
	// codexHomePath follows CODEX_HOME and would diagnose the developer's real
	// config instead of the temp home this test just wrote.
	t.Setenv("CODEX_HOME", "")
	registry := client.NewRegistry()
	if err := client.RegisterBuiltin(registry, home); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return Deps{
		Clients:  registry,
		Bindings: bindings.NewService(&bindings.Store{Root: root, DB: database.DB}),
	}
}
