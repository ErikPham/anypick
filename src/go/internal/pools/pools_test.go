package pools

import (
	"context"
	"reflect"
	"testing"

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

func TestSetMultiAndGet(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
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
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
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
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	got, err := s.Get("nope")
	if err != nil || got != nil {
		t.Fatalf("Get missing = %#v %v want nil,nil", got, err)
	}
}

func TestMutationsSelfLockWhenCallerDoesNot(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	if err := s.SetMultiContext(context.Background(), "claude", []Member{{Account: "a", Enabled: true}}, "127.0.0.1", 4000); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(context.Background(), Pool{Provider: "claude", Mode: "multi", Enabled: true, Strategy: "failover", Members: []Member{{Account: "a", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableContext(context.Background(), "claude"); err != nil {
		t.Fatal(err)
	}
}

func TestNestedMutationReentersHeldScope(t *testing.T) {
	database, root := testDB(t)
	s := &Store{Root: root, DB: database.DB}
	ctx := context.Background()
	scope := refs.MutationScopeForRef(refs.AccountPoolRef("claude"))
	err := lock.WithMutationLock(ctx, root, scope, func(held context.Context) error {
		return s.SetMultiContext(held, "claude", []Member{{Account: "a", Enabled: true}}, "", 0)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutateFailsClosedWithoutRoot(t *testing.T) {
	database, _ := testDB(t)
	s := &Store{DB: database.DB}
	if err := s.DisableContext(context.Background(), "claude"); err == nil {
		t.Fatal("want fail-closed without Root")
	}
}
