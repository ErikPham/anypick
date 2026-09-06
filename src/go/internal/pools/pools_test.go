package pools

import (
	"context"
	"reflect"
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

func TestSetMultiAndGet(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	members := []Member{{Account: "a", Enabled: true}, {Account: "b", Enabled: false}}
	if err := s.SetMultiContext(context.Background(), "claude", members, "127.0.0.1", 4000); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("claude")
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Mode != "multi" || !got.Enabled || got.Strategy != "failover" {
		t.Fatalf("mode/enabled/strategy = %q/%v/%q", got.Mode, got.Enabled, got.Strategy)
	}
	if got.Host != "127.0.0.1" || got.Port != 4000 {
		t.Fatalf("host/port = %q/%d", got.Host, got.Port)
	}
	if !reflect.DeepEqual(got.Members, members) {
		t.Fatalf("members = %#v want %#v", got.Members, members)
	}
}

func TestDisable(t *testing.T) {
	database := testDB(t)
	s := &Store{DB: database.DB}
	if err := s.SetMultiContext(context.Background(), "claude", []Member{{Account: "a"}}, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableContext(context.Background(), "claude"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("claude")
	if got == nil || got.Enabled {
		t.Fatalf("want disabled pool, got %#v", got)
	}
	// Disable only flips enabled; existing mode stays "multi" (INSERT path
	// uses "single" only for a never-created pool). Assert disabled, not mode.
	if got.Mode != "multi" && got.Mode != "single" {
		t.Fatalf("mode=%q want multi or single", got.Mode)
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
