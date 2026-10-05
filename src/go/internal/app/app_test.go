package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testHome is the client home every test in this package composes against.
//
// Options.Home exists so a test cannot reach the developer's real ~/.claude,
// ~/.codex and ~/.gemini, and that guarantee is only as good as its adoption:
// Ready with an empty Home binds every provider and client adapter to
// os.UserHomeDir(). A test here that forgets Home is not merely untidy, it
// reintroduces the exact hazard the seam closes, so compose through this
// helper rather than calling Ready with a bare Options.
func testHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestReadyCreatesRootAndClosesIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "anypick")
	a, closeFn, err := Ready(context.Background(), Options{Root: root, Home: testHome(t)})
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
	home := testHome(t)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, closeFn, err := Ready(context.Background(), Options{Root: root, Home: home})
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
