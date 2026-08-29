package profile

import (
	"testing"

	"github.com/outof0/anypick/go/internal/db"
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

func TestUpsertAndGet(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	rt := Runtime{
		Meta:    Meta{Name: "openrouter-main", Provider: "openrouter", Endpoint: "https://api.example.com"},
		Secrets: Secrets{APIKey: "sk-test"},
	}
	saved, err := s.Upsert(rt)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Meta.CreatedAt == "" || saved.Meta.UpdatedAt == "" {
		t.Fatalf("timestamps empty: %#v", saved.Meta)
	}
	got, err := s.Get("openrouter-main")
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Meta.Provider != "openrouter" || got.Meta.Endpoint != "https://api.example.com" {
		t.Fatalf("meta = %#v", got.Meta)
	}
	if got.Secrets.APIKey != "sk-test" {
		t.Fatalf("secrets = %#v", got.Secrets)
	}
	created := saved.Meta.CreatedAt
	// Second upsert must preserve CreatedAt, bump UpdatedAt.
	saved.Meta.Endpoint = "https://api2.example.com"
	saved2, err := s.Upsert(saved)
	if err != nil {
		t.Fatal(err)
	}
	if saved2.Meta.CreatedAt != created {
		t.Fatalf("CreatedAt changed %q -> %q", created, saved2.Meta.CreatedAt)
	}
}

func TestGetMissingReturnsNil(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	got, err := s.Get("nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing = %#v %v want nil,nil", got, err)
	}
}

func TestListAndDelete(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	for _, name := range []string{"b", "a", "c"} {
		if _, err := s.Upsert(Runtime{Meta: Meta{Name: name, Provider: "openrouter"}}); err != nil {
			t.Fatal(err)
		}
	}
	names, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("List=%v want [a b c]", names)
	}
	if err := s.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("b"); got != nil {
		t.Fatalf("want deleted, got %#v", got)
	}
}
