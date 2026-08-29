package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadyCreatesRootAndClosesIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "anypick")
	a, closeFn, err := Ready(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if a.Root != root {
		t.Fatalf("root = %q, want %q", a.Root, root)
	}
	closeFn()
	closeFn() // second close must be safe
	if _, err := os.Stat(filepath.Join(root, "anypick.db")); err != nil {
		t.Fatalf("database was not created under root: %v", err)
	}
}

func TestReadyConcurrentBootstrapSerializes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "anypick")
	const n = 4
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, closeFn, err := Ready(context.Background(), Options{Root: root})
			if err != nil {
				errCh <- err
				return
			}
			closeFn()
			errCh <- nil
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("concurrent bootstrap failed: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for concurrent bootstrap")
		}
	}
}

func TestMigrateLegacyRootIfPristineCopiesUserData(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".anypick")
	legacy := filepath.Join(base, ".hotplug")
	for _, d := range []string{root, legacy} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy store with one user table.
	srcDB := filepath.Join(legacy, "hotplug.db")
	d, err := openSQLite(srcDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`CREATE TABLE accounts (name TEXT); INSERT INTO accounts VALUES ('work')`); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyRootIfPristine(root); err != nil {
		t.Fatal(err)
	}
	migrated := hasUserData(filepath.Join(root, "anypick.db"))
	if !migrated {
		t.Fatal("user data was not migrated into the anypick root")
	}
	// Source stays intact.
	if !hasUserData(srcDB) {
		t.Fatal("legacy source database must remain intact")
	}
}

func TestMigrateLegacyRootSkipsDirtyDestination(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".anypick")
	legacy := filepath.Join(base, ".hotplug")
	for _, d := range []string{filepath.Join(root, "providers", "claude"), legacy} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "claude", "x"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirEntries(root)
	if err := MigrateLegacyRootIfPristine(root); err != nil {
		t.Fatal(err)
	}
	after := dirEntries(root)
	if len(before) != len(after) {
		t.Fatalf("dirty destination was overwritten: %v -> %v", before, after)
	}
}
