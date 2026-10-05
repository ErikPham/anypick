package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Get rebuilds the snapshot cache by deleting it first. A caller that already
// holds the provider lock (Use, save, delete) must not observe that gap, and
// a second Get must wait until the rebuild finishes.
func TestGetMaterializeWaitsForProviderLock(t *testing.T) {
	s := testStore(t)
	snap, _, err := s.PrepareSnapshot("gemini", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "oauth_creds.json"), []byte(`{"token":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetaContext(context.Background(), meta("gemini", "a"), snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "gemini", "a")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	cache := filepath.Join(got.SnapshotDir, "oauth_creds.json")

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		err := s.mutate(context.Background(), "gemini", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
		if err != nil {
			t.Errorf("mutate: %v", err)
		}
	}()
	<-held

	done := make(chan struct{})
	go func() {
		_, err := s.Get(context.Background(), "gemini", "a")
		if err != nil {
			t.Errorf("Get: %v", err)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Get returned while the provider lock was held")
	case <-time.After(300 * time.Millisecond):
	}
	body, err := os.ReadFile(cache)
	if err != nil || string(body) != `{"token":"kept"}` {
		t.Fatalf("snapshot cache during the lock = %q, %v", body, err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Get did not return after the provider lock was released")
	}
	body, err = os.ReadFile(cache)
	if err != nil || string(body) != `{"token":"kept"}` {
		t.Fatalf("snapshot cache after Get = %q, %v", body, err)
	}
}
