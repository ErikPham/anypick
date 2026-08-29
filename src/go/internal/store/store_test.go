package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/slug"
)

func testStore(t *testing.T) *AccountStore {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(root, database)
}

func meta(provider, name string) Meta {
	return Meta{Name: name, Provider: provider, CreatedAt: nowISO(), UpdatedAt: nowISO()}
}

// INVARIANT #4: writeMeta is the atomic commit point. A failed ingest must
// leave the previous snapshot rows + metadata exactly as they were.
func TestWriteMetaAtomicCommit(t *testing.T) {
	s := testStore(t)

	snap1, _, err := s.PrepareSnapshot("grok", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap1, "auth.json"), []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMeta(meta("grok", "work"), snap1); err != nil {
		t.Fatal(err)
	}

	// Second save whose staging path becomes an unreadable file mid-flight →
	// the transaction rolls back and the first snapshot survives.
	snap2, _, err := s.PrepareSnapshot("grok", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(snap2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snap2, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMeta(Meta{Name: "work", Provider: "grok"}, snap2); err == nil {
		t.Fatal("expected ingest failure to abort commit")
	}

	got, err := s.Get("grok", "work")
	if err != nil || got == nil {
		t.Fatalf("account vanished after failed write: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(got.SnapshotDir, "auth.json"))
	if err != nil || string(content) != `{"v":1}` {
		t.Fatalf("previous snapshot not preserved: %q %v", content, err)
	}
}

func TestWriteMetaReplacesSnapshotAtomically(t *testing.T) {
	s := testStore(t)
	snap, _, err := s.PrepareSnapshot("codex", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMeta(meta("codex", "personal"), snap); err != nil {
		t.Fatal(err)
	}

	snap2, _, err := s.PrepareSnapshot("codex", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap2, "auth.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap2, "extra.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMeta(meta("codex", "personal"), snap2); err != nil {
		t.Fatal(err)
	}

	got, err := s.Require("codex", "personal")
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(got.SnapshotDir, "auth.json"))
	if err != nil || string(old) != "new" {
		t.Fatalf("snapshot not replaced: %q %v", old, err)
	}
	if _, err := os.Stat(filepath.Join(got.SnapshotDir, "extra.txt")); err != nil {
		t.Fatal("added file missing after re-ingest")
	}
	rows := 0
	if err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM account_snapshot_files WHERE provider='codex' AND account='personal'`,
	).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("blob rows = %d err=%v, want 2", rows, err)
	}
}

func TestDeleteClearsActivePointer(t *testing.T) {
	s := testStore(t)
	snap, _, err := s.PrepareSnapshot("gemini", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMeta(meta("gemini", "a"), snap); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive("gemini", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gemini", "a"); err != nil {
		t.Fatal(err)
	}
	active, err := s.GetActive("gemini")
	if err != nil || active != "" {
		t.Fatalf("active pointer survived delete: %q %v", active, err)
	}
	if _, err := s.Require("gemini", "a"); err == nil {
		t.Fatal("account still present after delete")
	}
}

func TestPatchMeta(t *testing.T) {
	s := testStore(t)
	snap, _, _ := s.PrepareSnapshot("grok", "w")
	_ = os.WriteFile(filepath.Join(snap, "f"), []byte("x"), 0o600)
	if err := s.WriteMeta(meta("grok", "w"), snap); err != nil {
		t.Fatal(err)
	}
	next, err := s.PatchMeta("grok", "w", MetaPatch{LabelSet: true, Label: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Label != "Work" {
		t.Fatalf("label = %q", next.Label)
	}
	got, _ := s.Require("grok", "w")
	if got.Meta.Label != "Work" {
		t.Fatalf("stored label = %q", got.Meta.Label)
	}
}

func TestNormalizeSlugParity(t *testing.T) {
	cases := map[string]string{
		"Kiro Key - 1k": "kiro-key-1k",
		"Work Team":     "work-team",
		"café":          "cafe",
		"a/b\\c":        "a-b-c",
		"..hidden..":    "hidden",
		"--leading--":   "leading",
	}
	for in, want := range cases {
		got, err := normalizeAccountNameErr(in)
		if err != nil {
			t.Errorf("normalize(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := normalizeAccountNameErr("///"); err == nil {
		t.Error("empty slug must error")
	}
}

func normalizeAccountNameErr(raw string) (string, error) { return slug.AccountName(raw) }
