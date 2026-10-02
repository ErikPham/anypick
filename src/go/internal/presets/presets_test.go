package presets

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

func testDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, root
}

func specFor(provider, account string) bindings.Spec {
	return bindings.Spec{
		Client:          "claude",
		Source:          refs.AccountRef(provider, account),
		Model:           bindings.OmittedModel(),
		TransportPolicy: "auto",
	}
}

func TestUpsertNewAndGet(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	spec := specFor("grok", "work")
	saved, err := s.UpsertContext(context.Background(), "my-preset", spec)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Revision != 1 {
		t.Fatalf("saved = %#v want id non-empty, revision 1", saved)
	}
	if saved.CreatedAt == "" || saved.UpdatedAt == "" {
		t.Fatalf("timestamps empty: %#v", saved)
	}
	got, err := s.GetByName("my-preset")
	if err != nil || got == nil {
		t.Fatalf("GetByName: %v %v", got, err)
	}
	if got.ID != saved.ID || got.Spec.Source != spec.Source {
		t.Fatalf("GetByName = %#v want ID %q source %#v", got, saved.ID, spec.Source)
	}
	if ok, _ := s.Exists("my-preset"); !ok {
		t.Fatalf("Exists want true")
	}
	if ok, _ := s.Exists("nope"); ok {
		t.Fatalf("Exists want false for missing")
	}
}

func TestUpsertBumpsRevision(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	spec1 := specFor("grok", "work")
	saved1, _ := s.UpsertContext(context.Background(), "p", spec1)
	spec2 := specFor("grok", "personal")
	saved2, err := s.UpsertContext(context.Background(), "p", spec2)
	if err != nil {
		t.Fatal(err)
	}
	if saved2.ID != saved1.ID {
		t.Fatalf("ID changed %q -> %q", saved1.ID, saved2.ID)
	}
	if saved2.Revision != 2 {
		t.Fatalf("Revision=%d want 2", saved2.Revision)
	}
	if saved2.CreatedAt != saved1.CreatedAt {
		t.Fatalf("CreatedAt changed %q -> %q", saved1.CreatedAt, saved2.CreatedAt)
	}
	if saved2.Spec.Source != spec2.Source {
		t.Fatalf("spec not updated: %#v", saved2.Spec)
	}
}

func TestMarkUsed(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	saved, _ := s.UpsertContext(context.Background(), "p", specFor("grok", "work"))
	if err := s.MarkUsedContext(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetByName("p")
	if got.UseCount != 1 || got.LastUsedAt == "" {
		t.Fatalf("after MarkUsed: useCount=%d lastUsedAt=%q", got.UseCount, got.LastUsedAt)
	}
	if err := s.MarkUsedContext(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetByName("p")
	if got2.UseCount != 2 {
		t.Fatalf("UseCount=%d want 2", got2.UseCount)
	}
}

func TestDeleteByName(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	if _, err := s.UpsertContext(context.Background(), "p", specFor("grok", "work")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteByNameContext(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetByName("p"); got != nil {
		t.Fatalf("want deleted, got %#v", got)
	}
}

func TestSourceRefOf(t *testing.T) {
	p := Saved{Spec: specFor("claude", "work")}
	ref := SourceRefOf(p)
	if ref.Kind != refs.KindAccount || ref.Provider != "claude" || ref.Name != "work" {
		t.Fatalf("SourceRefOf=%#v", ref)
	}
}

func TestMutationsSelfLockWhenCallerDoesNot(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	saved, err := s.UpsertContext(context.Background(), "p", specFor("grok", "work"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUsedContext(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteByNameContext(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
}

func TestNestedMutationReentersHeldScope(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	ctx := context.Background()
	scope := refs.MutationScopeForRef(refs.PresetRef("p"))
	err := lock.WithMutationLock(ctx, root, scope, func(held context.Context) error {
		_, err := s.UpsertContext(held, "p", specFor("grok", "work"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutateFailsClosedWithoutRoot(t *testing.T) {
	database, _ := testDB(t)
	s := &Store{DB: database.DB}
	_, err := s.UpsertContext(context.Background(), "p", specFor("grok", "work"))
	if err == nil {
		t.Fatal("want fail-closed without Root")
	}
}
