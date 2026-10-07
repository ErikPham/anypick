package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// A display read must return while a browser sign-in holds the provider scope.
// Get is the wrong read for that path: it takes the lock to re-materialize the
// snapshot cache, so it burns the 5s mutation-lock budget and then fails with
// MUTATION_LOCK_TIMEOUT. CachedAccount returns the same metadata and paths from
// SQLite without the lock.
func TestCachedAccountReturnsWhileProviderLockHeld(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	snap, _, err := s.PrepareSnapshot("grok", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"), []byte(`{"token":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := meta("grok", "work")
	meta.Label = "Studio"
	meta.Identity = "alex@studio.example"
	if err := s.WriteMetaContext(ctx, meta, snap); err != nil {
		t.Fatal(err)
	}
	want, err := s.Get(ctx, "grok", "work")
	if err != nil || want == nil {
		t.Fatal(err)
	}
	// The on-disk cache is a cache: a display read must answer with the
	// directory absent, because materialization is exactly what Get adds.
	if err := os.RemoveAll(want.SnapshotDir); err != nil {
		t.Fatal(err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- lock.WithMutationLock(context.Background(), s.Root,
			refs.ProviderScope("grok"), func(context.Context) error {
				close(held)
				select {
				case <-release:
				case <-time.After(7 * time.Second):
				}
				return nil
			})
	}()
	<-held

	type readResult struct {
		account *Account
		err     error
		elapsed time.Duration
	}
	done := make(chan readResult, 1)
	start := time.Now()
	go func() {
		got, gerr := s.CachedAccount(context.Background(), "grok", "work")
		done <- readResult{got, gerr, time.Since(start)}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("CachedAccount while locked: %v", got.err)
		}
		if got.account == nil {
			t.Fatal("CachedAccount returned no account")
		}
		// Same fields the display paths read, byte for byte.
		if got.account.Meta.Label != want.Meta.Label ||
			got.account.Meta.Identity != want.Meta.Identity ||
			got.account.Meta.Name != want.Meta.Name ||
			got.account.Meta.Provider != want.Meta.Provider ||
			got.account.SnapshotDir != want.SnapshotDir ||
			got.account.Proxy.Enabled != want.Proxy.Enabled {
			t.Fatalf("CachedAccount = %+v, want the same display fields as Get %+v", got.account, want)
		}
		t.Logf("CachedAccount while locked: elapsed=%s", got.elapsed)
		if got.elapsed > 50*time.Millisecond {
			t.Fatalf("CachedAccount took %s while the provider lock was held", got.elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CachedAccount did not return while the provider lock was held")
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("release provider scope: %v", err)
	}
}

// Get still materializes the snapshot cache. A caller that needs real files on
// disk — Restore, export, fingerprinting — depends on that, so the display
// read's lock-free path must not become the only read.
func TestGetMaterializesDeletedSnapshotDir(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	snap, _, err := s.PrepareSnapshot("grok", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"), []byte(`{"token":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetaContext(ctx, meta("grok", "work"), snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "grok", "work")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	cached, err := s.CachedAccount(ctx, "grok", "work")
	if err != nil || cached == nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cached.SnapshotDir); err != nil {
		t.Fatal(err)
	}

	// The cache read is metadata-only: it reports the path without rebuilding.
	if body, err := os.ReadFile(filepath.Join(cached.SnapshotDir, "auth.json")); err == nil {
		t.Fatalf("CachedAccount materialized the snapshot: %q", body)
	}
	if _, err := os.Stat(cached.SnapshotDir); !os.IsNotExist(err) {
		t.Fatalf("CachedAccount created the snapshot dir: %v", err)
	}

	// Get restores it from the committed rows.
	again, err := s.Get(ctx, "grok", "work")
	if err != nil || again == nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(again.SnapshotDir, "auth.json"))
	if err != nil || string(body) != `{"token":"kept"}` {
		t.Fatalf("Get did not re-materialize the snapshot: %q, %v", body, err)
	}
}
