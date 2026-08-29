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
