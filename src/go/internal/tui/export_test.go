package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/store"
)

// TestExportLandsUnderDataRoot runs the real export command against an
// isolated app and asserts the envelope lands under the AnyPick data root,
// never a browsed folder: the envelope holds live credentials in plaintext,
// so ~/Downloads is the wrong default even at 0600.
func TestExportLandsUnderDataRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ANYPICK_HOME", tmp)
	t.Setenv("HOME", tmp)

	a, cleanup, err := app.Ready(context.Background(), app.Options{SkipMigrate: true})
	if err != nil {
		t.Fatalf("app.Ready: %v", err)
	}
	defer cleanup()

	snapshotDir, _, err := a.Store.PrepareSnapshot("codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "auth.json"), []byte(`{"tokens":{"access_token":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := store.Meta{
		Name: "work", Provider: "codex",
		CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	if err := a.Store.WriteMetaContext(context.Background(), meta, snapshotDir); err != nil {
		t.Fatal(err)
	}
	_ = a.Store.DiscardPreparedSnapshot(snapshotDir)

	want := filepath.Join(tmp, "exports", "anypick-codex-work.json")
	if got := defaultExportPath(a.Root, "codex", "work"); got != want {
		t.Fatalf("defaultExportPath = %q, want %q", got, want)
	}
	if got := defaultExportPath("", "codex", "work"); got != want {
		t.Fatalf("defaultExportPath(\"\") = %q, want data-root fallback %q", got, want)
	}

	msg := exportAccountCmd(context.Background(), a, "codex", "work")()
	outcome, ok := msg.(actionMsg)
	if !ok {
		t.Fatalf("export command returned %T, want actionMsg", msg)
	}
	if outcome.err != nil {
		t.Fatalf("export failed: %v", outcome.err)
	}
	if outcome.text != "exported codex/work → "+want {
		t.Fatalf("export status = %q, want the exact path %q surfaced", outcome.text, want)
	}
	if strings.Contains(want, "Downloads") {
		t.Fatalf("export default %q sits in a browsed directory", want)
	}
	if fi, serr := os.Stat(want); serr != nil || fi.IsDir() {
		t.Fatalf("export wrote no file at %q (stat err %v)", want, serr)
	}
}
