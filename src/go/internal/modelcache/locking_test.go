package modelcache

import (
	"context"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/lock"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func timeAfter() <-chan time.Time { return time.After(2 * time.Second) }

// This package deliberately does NOT self-lock. Its package doc states the
// contract: "The store owns its SQL and takes no locks — the discovery service
// coordinates them (ADR 0009)." The shared coordinator gate cannot be used
// here, because it reports every method whose name reads as a mutation and
// would demand a lock this package is not supposed to take.
//
// The risk that contract creates is a caller reaching Write or Clear without
// holding the scope. The doctor's rebuild_caches did exactly that: it called
// Clear from internal/doctor with no lock, and nothing in this package could
// see it. The fix put the lock on that caller. This test pins the contract
// itself, so the next caller has a written statement of what it must do.
func TestStoreDoesNotSelfLockAndCallersMustCoordinate(t *testing.T) {
	root := t.TempDir()
	database := openTestDB(t)
	store := &Store{DB: database}

	// Hold the scope the callers use, then prove a bare Clear from a fresh
	// context does NOT re-enter it: if this package self-locked, the call would
	// block or fail here, and the contract documented above would be wrong.
	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = lock.WithMutationLock(context.Background(), root, "model-cache", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	done := make(chan error, 1)
	go func() { done <- store.Clear(context.Background(), "probe") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Clear returned %v; it must execute without the lock, because the caller holds it", err)
		}
	case <-timeAfter():
		t.Fatal("Clear blocked on the model-cache scope: this package must not self-lock, or every documented caller deadlocks")
	}
	close(release)
}
