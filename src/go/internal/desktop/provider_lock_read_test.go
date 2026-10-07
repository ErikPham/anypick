package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// displayReadBudget bounds one display read while a provider lock is held. A
// read that still took the lock cannot finish inside it: the mutation-lock
// budget is 5s (internal/lock), so it either blocks for 5.006s or fails.
const displayReadBudget = 50 * time.Millisecond

// snapshotBuilderFor returns a builder without running a full snapshot, so a
// test can time one display read in isolation.
func snapshotBuilderFor(env *trayEnv) *snapshotBuilder {
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	return &snapshotBuilder{
		srv:        srv,
		registry:   map[string]actionHandler{},
		modelRoles: map[string]modelRoleTarget{},
	}
}

// saveGrokAccountAndClearLive saves one grok account, then removes the live
// auth so the snapshot's usage section does not poll xAI.
func saveGrokAccountAndClearLive(t *testing.T, env *trayEnv) {
	t.Helper()
	authPath := filepath.Join(env.Home, ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", authPath)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"sess":{"email":"dev@grok.xai","access_token":"t1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Svc.Save(context.Background(), "grok", "work", false); err != nil {
		t.Fatalf("save grok: %v", err)
	}
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
}

// holdProviderScope holds refs.ProviderScope(providerID) until the returned
// release func runs, failing the test if the scope cannot be acquired.
func holdProviderScope(t *testing.T, root, providerID string) func() {
	t.Helper()
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lock.WithMutationLock(context.Background(), root,
			refs.ProviderScope(providerID), func(context.Context) error {
				close(held)
				<-release
				return nil
			})
	}()
	<-held
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("release provider scope: %v", err)
		}
	}
}

// A browser sign-in holds refs.ProviderScope for its whole user-paced flow
// (browserlogin.DefaultTimeout, ten minutes) while the desktop snapshot builder
// runs on a 2.5s ticker. The display reads it makes must therefore return
// without that lock. Before the display reads were moved off Get, the grok
// proxy row blocked for the 5s lock budget and then fell back to the
// "provider/name proxy" label because Get had failed with
// MUTATION_LOCK_TIMEOUT.
func TestSnapshotDisplayReadsDoNotBlockOnProviderLock(t *testing.T) {
	env := newTrayEnv(t)
	saveGrokAccountAndClearLive(t, env)
	ctx := context.Background()

	b := snapshotBuilderFor(env)
	// Warm nothing: the reads below must not need the lock even cold.
	release := holdProviderScope(t, env.Root, "grok")
	defer release()

	start := time.Now()
	label := proxyRowLabel(ctx, b, "grok", "work")
	labelElapsed := time.Since(start)
	t.Logf("proxyRowLabel while grok scope held: %q elapsed=%s", label, labelElapsed)
	if labelElapsed > displayReadBudget {
		t.Fatalf("proxyRowLabel took %s while the provider lock was held; budget %s",
			labelElapsed, displayReadBudget)
	}
	// The identity proves the row read the saved account. A failed Get leaves
	// the provider/name fallback, which is what the lock-taking read produced.
	if !strings.Contains(label, "dev@grok.xai") {
		t.Fatalf("grok/work proxy label = %q, want the saved identity", label)
	}

	start = time.Now()
	routeKind := b.routeKindForSource(ctx, "claude", refs.AccountRef("grok", "work"))
	routeElapsed := time.Since(start)
	t.Logf("routeKindForSource while grok scope held: %q elapsed=%s", routeKind, routeElapsed)
	if routeElapsed > displayReadBudget {
		t.Fatalf("routeKindForSource took %s while the provider lock was held; budget %s",
			routeElapsed, displayReadBudget)
	}
	if routeKind != "managed-account" {
		t.Fatalf("routeKind = %q, want managed-account from the saved Proxy.Enabled", routeKind)
	}
}

// The whole snapshot must still build while a sign-in holds the provider scope.
// The bound is relative to this machine's unlocked build so it measures the
// lock, not the builder's ordinary cost.
func TestSnapshotBuildsWhileProviderLockIsHeld(t *testing.T) {
	env := newTrayEnv(t)
	saveGrokAccountAndClearLive(t, env)
	ctx := context.Background()
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})

	start := time.Now()
	if _, err := srv.buildSnapshot(ctx); err != nil {
		t.Fatalf("baseline buildSnapshot: %v", err)
	}
	baseline := time.Since(start)
	t.Logf("unlocked buildSnapshot baseline: %s", baseline)

	release := holdProviderScope(t, env.Root, "grok")
	defer release()

	start = time.Now()
	snap, err := srv.buildSnapshot(ctx)
	elapsed := time.Since(start)
	t.Logf("buildSnapshot while grok scope held: elapsed=%s", elapsed)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	// The lock budget alone is 5s; any read still taking it blows past this.
	if budget := baseline + time.Second; elapsed > budget {
		t.Fatalf("buildSnapshot took %s while the provider lock was held; budget %s (baseline %s)",
			elapsed, budget, baseline)
	}

	for _, row := range snap.Proxies {
		if row.ID == "grok/work" && !strings.Contains(row.Label, "dev@grok.xai") {
			t.Fatalf("grok/work proxy label = %q, want the saved identity", row.Label)
		}
	}
}
