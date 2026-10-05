package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/provider"
)

// TestMain pins the ADR-0025 app-root guard for this test binary. The
// provider default is already a no-op and compose skips Enable under
// testing.Testing(); this keeps the same contract visible at the
// composition-root package even if that default ever flips.
func TestMain(m *testing.M) {
	provider.AntigravityKeyringPut = func(*provider.GeminiAntigravityFile) error { return nil }
	os.Exit(m.Run())
}

// TestReadyKeepsKeyringNoopUnderTest proves the ADR-0025 composition-root
// gate: Ready/compose must not install the production Keychain writer while
// testing.Testing() is true. A canary hook would be replaced by Enable if
// the gate were missing.
func TestReadyKeepsKeyringNoopUnderTest(t *testing.T) {
	var consulted int
	previous := provider.AntigravityKeyringPut
	provider.AntigravityKeyringPut = func(*provider.GeminiAntigravityFile) error {
		consulted++
		return nil
	}
	t.Cleanup(func() { provider.AntigravityKeyringPut = previous })

	root := filepath.Join(t.TempDir(), "anypick")
	_, closeFn, err := Ready(context.Background(), Options{Root: root, Home: testHome(t), SkipMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	closeFn()

	if err := provider.AntigravityKeyringPut(&provider.GeminiAntigravityFile{}); err != nil {
		t.Fatalf("canary hook errored: %v", err)
	}
	if consulted != 1 {
		t.Fatalf("compose replaced the test keyring hook (consulted=%d); real Keychain may be reachable", consulted)
	}
}
