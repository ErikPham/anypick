package bindings

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/refs"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func spec() Spec {
	return Spec{
		Client:          "claude",
		Source:          refs.AccountRef("grok", "work"),
		Model:           OmittedModel(),
		TransportPolicy: "auto",
	}
}

func TestGlobalUpsertGetDelete(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	g := Global{
		Client:     "claude",
		Spec:       spec(),
		Provenance: Provenance{Kind: "direct"},
	}
	saved, err := s.UpsertGlobalContext(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CreatedAt == "" || saved.UpdatedAt == "" {
		t.Fatalf("timestamps empty: %#v", saved)
	}
	got, err := s.GetGlobalContext(context.Background(), "claude")
	if err != nil || got == nil {
		t.Fatalf("GetGlobal: %v %v", got, err)
	}
	if got.Spec.Source != g.Spec.Source {
		t.Fatalf("source = %#v want %#v", got.Spec.Source, g.Spec.Source)
	}
	created := saved.CreatedAt
	g.Spec.Model = ExplicitModel("grok-1")
	saved2, err := s.UpsertGlobalContext(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if saved2.CreatedAt != created {
		t.Fatalf("CreatedAt changed %q -> %q", created, saved2.CreatedAt)
	}
	if saved2.Spec.Model.ID != "grok-1" {
		t.Fatalf("model not updated: %#v", saved2.Spec.Model)
	}
	if err := s.DeleteGlobalContext(context.Background(), "claude"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetGlobalContext(context.Background(), "claude"); got != nil {
		t.Fatalf("want deleted, got %#v", got)
	}
}

func TestGlobalGetMissingNil(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	got, err := s.GetGlobalContext(context.Background(), "nope")
	if err != nil || got != nil {
		t.Fatalf("GetGlobal missing = %#v %v want nil,nil", got, err)
	}
}

func TestProjectUpsertGetDelete(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	p := Project{
		ProjectRoot: "/tmp/proj",
		Client:      "claude",
		Spec:        spec(),
		Provenance:  Provenance{Kind: "direct"},
	}
	saved, err := s.UpsertProjectContext(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CreatedAt == "" {
		t.Fatalf("CreatedAt empty")
	}
	got, err := s.GetProjectContext(context.Background(), "/tmp/proj", "claude")
	if err != nil || got == nil {
		t.Fatalf("GetProject: %v %v", got, err)
	}
	if got.ProjectRoot != "/tmp/proj" || got.Client != "claude" {
		t.Fatalf("project = %#v", got)
	}
	// Update preserves CreatedAt.
	p.Spec.Model = ExplicitModel("grok-2")
	saved2, _ := s.UpsertProjectContext(context.Background(), p)
	if saved2.CreatedAt != saved.CreatedAt {
		t.Fatalf("CreatedAt changed")
	}
	if err := s.DeleteProjectContext(context.Background(), "/tmp/proj", "claude"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProjectContext(context.Background(), "/tmp/proj", "claude"); got != nil {
		t.Fatalf("want deleted, got %#v", got)
	}
}

func TestExplicitAndOmittedModel(t *testing.T) {
	if m := ExplicitModel("x"); m.Mode != "explicit" || m.ID != "x" {
		t.Fatalf("ExplicitModel=%#v", m)
	}
	if m := OmittedModel(); m.Mode != "omitted" {
		t.Fatalf("OmittedModel=%#v", m)
	}
}
