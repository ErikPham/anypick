package modelcache

import (
	"context"
	"reflect"
	"testing"

	"github.com/outof0/anypick/go/internal/db"
)

// testStore opens a migrated database under a temp root. Nothing here may
// reach a developer's real ~/.anypick.
func testStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Store{DB: database}
}

func TestWriteThenGetRoundTrips(t *testing.T) {
	s := testStore(t)
	want := &Entry{
		Provider:  "deepseek",
		Endpoint:  "https://api.deepseek.com/v1",
		Models:    []string{"deepseek-chat", "deepseek-reasoner"},
		FetchedAt: "2026-08-30T01:02:03Z",
	}
	if err := s.Write(context.Background(), want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.Get(context.Background(), want.Provider, want.Endpoint)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get = nil after Write, want the cached entry")
	}
	if !reflect.DeepEqual(got.Models, want.Models) {
		t.Errorf("Models = %v, want %v", got.Models, want.Models)
	}
	if got.FetchedAt != want.FetchedAt {
		t.Errorf("FetchedAt = %q, want %q", got.FetchedAt, want.FetchedAt)
	}
}

// A miss is the common case on a cold start; it must not be an error, or every
// first discovery would look like a failure.
func TestMissReturnsNilWithoutError(t *testing.T) {
	s := testStore(t)
	got, err := s.Get(context.Background(), "nope", "https://nope.invalid/v1")
	if err != nil {
		t.Fatalf("Get on a miss = %v, want nil error", err)
	}
	if got != nil {
		t.Errorf("Get on a miss = %+v, want nil", got)
	}
}

// Corruption is advisory: the documented contract is "a corrupt row reads as
// no cache", not "surface an error". Deleting the table at any time must be
// safe, so this is the behaviour that makes that true.
func TestCorruptRowReadsAsAMiss(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO model_catalog_cache (provider, endpoint, models_json, fetched_at)
		 VALUES (?, ?, ?, ?)`,
		"deepseek", "https://api.deepseek.com/v1", "{not json", "2026-08-30T01:02:03Z"); err != nil {
		t.Fatalf("seed a corrupt row: %v", err)
	}
	got, err := s.Get(ctx, "deepseek", "https://api.deepseek.com/v1")
	if err != nil {
		t.Fatalf("Get on a corrupt row = %v, want nil error (advisory data)", err)
	}
	if got != nil {
		t.Errorf("Get on a corrupt row = %+v, want nil", got)
	}
}

// The primary key is (provider, endpoint): the same provider id can point at
// two hosts serving different lists, and each must cache independently.
func TestEndpointIsPartOfTheKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	eu := &Entry{Provider: "openai", Endpoint: "https://eu.example/v1",
		Models: []string{"a"}, FetchedAt: "t1"}
	us := &Entry{Provider: "openai", Endpoint: "https://us.example/v1",
		Models: []string{"b", "c"}, FetchedAt: "t2"}
	for _, e := range []*Entry{eu, us} {
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write %s: %v", e.Endpoint, err)
		}
	}
	gotEU, err := s.Get(ctx, eu.Provider, eu.Endpoint)
	if err != nil {
		t.Fatalf("Get eu: %v", err)
	}
	gotUS, err := s.Get(ctx, us.Provider, us.Endpoint)
	if err != nil {
		t.Fatalf("Get us: %v", err)
	}
	if gotEU == nil || gotUS == nil {
		t.Fatal("one endpoint's write clobbered the other's row")
	}
	if len(gotEU.Models) != 1 || gotEU.Models[0] != "a" {
		t.Errorf("eu Models = %v, want [a]", gotEU.Models)
	}
	if len(gotUS.Models) != 2 {
		t.Errorf("us Models = %v, want 2 entries", gotUS.Models)
	}
}

func TestWriteUpsertsRatherThanDuplicating(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := &Entry{Provider: "groq", Endpoint: "https://api.groq.com/v1",
		Models: []string{"old"}, FetchedAt: "t1"}
	if err := s.Write(ctx, p); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.Models = []string{"new1", "new2"}
	p.FetchedAt = "t2"
	if err := s.Write(ctx, p); err != nil {
		t.Fatalf("Write (upsert): %v", err)
	}
	got, err := s.Get(ctx, p.Provider, p.Endpoint)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get = nil after upsert")
	}
	if !reflect.DeepEqual(got.Models, []string{"new1", "new2"}) {
		t.Errorf("Models = %v, want the second write to win", got.Models)
	}
	if got.FetchedAt != "t2" {
		t.Errorf("FetchedAt = %q, want t2", got.FetchedAt)
	}
}

func TestClearIsScopedToOneProvider(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	keep := &Entry{Provider: "keep", Endpoint: "https://keep/v1",
		Models: []string{"k"}, FetchedAt: "t1"}
	drop := &Entry{Provider: "drop", Endpoint: "https://drop/v1",
		Models: []string{"d"}, FetchedAt: "t1"}
	for _, e := range []*Entry{keep, drop} {
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write %s: %v", e.Provider, err)
		}
	}
	if err := s.Clear(ctx, "drop"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got, _ := s.Get(ctx, drop.Provider, drop.Endpoint); got != nil {
		t.Errorf("cleared provider still cached: %+v", got)
	}
	if got, _ := s.Get(ctx, keep.Provider, keep.Endpoint); got == nil {
		t.Error("Clear(drop) removed the unrelated provider's cache")
	}
}

func TestClearWithEmptyProviderDropsEverything(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, e := range []*Entry{
		{Provider: "a", Endpoint: "https://a/v1", Models: []string{"1"}, FetchedAt: "t1"},
		{Provider: "b", Endpoint: "https://b/v1", Models: []string{"2"}, FetchedAt: "t1"},
	} {
		if err := s.Write(ctx, e); err != nil {
			t.Fatalf("Write %s: %v", e.Provider, err)
		}
	}
	if err := s.Clear(ctx, ""); err != nil {
		t.Fatalf("Clear all: %v", err)
	}
	for _, p := range []string{"a", "b"} {
		if got, _ := s.Get(ctx, p, "https://"+p+"/v1"); got != nil {
			t.Errorf("provider %s survived Clear(\"\"): %+v", p, got)
		}
	}
}
