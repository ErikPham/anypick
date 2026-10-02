package activation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
)

// An unknown live comparison is not evidence the snapshot is already current.
// Skipping the restore there leaves the official app on a file DetectLive
// merely saw (the Claude token-rotation case is the known-match path and
// stays skipped).
func TestWriteNativeAuthRestoresWhenComparisonIsUnknown(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	authPath := filepath.Join(env.Home, ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	saved := []byte(`{"sessions":{"s1":{"email":"old@example.com","access_token":"tok-old"}}}`)
	if err := os.WriteFile(authPath, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Svc.Save(ctx, "grok", "keep", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Svc.Store.SetActiveContext(ctx, "grok", "keep"); err != nil {
		t.Fatal(err)
	}
	drifted := []byte(`{"sessions":{"s1":{"email":"other@example.com","access_token":"tok-other"}}}`)
	if err := os.WriteFile(authPath, drifted, 0o600); err != nil {
		t.Fatal(err)
	}
	cur, err := env.Svc.Current(ctx, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Active != "keep" || !cur.IsLiveMatch || cur.LiveMatchKnown {
		t.Fatalf("fixture current = active %q match %v known %v", cur.Active, cur.IsLiveMatch, cur.LiveMatchKnown)
	}

	entry, err := env.Deps.Journal.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		State: journal.StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Mode:           ModePersistent,
		Client:         "codex",
		ResolvedSource: &ResolvedSource{Ref: refs.AccountRef("grok", "keep")},
	}
	if err := writeNativeAuth(ctx, plan, env.Deps, &activationContext{}, entry.ID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(saved) {
		t.Fatalf("live auth = %s, want the saved snapshot", got)
	}
}

// A cancel after the hub port is persisted must still put that port back.
// The undo runs after the request context is done.
func TestStartHubProxyUndoClearsPortAfterCancel(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: "h", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Client:         "codex",
		Mode:           ModePersistent,
		ResolvedSource: &ResolvedSource{Ref: refs.ProxyHubRef("h")},
		Transport:      ResolvedTransport{ManagedProxy: &ManagedProxyRef{}},
	}
	actx := &activationContext{}
	if err := startHubProxy(ctx, plan, env.Deps, actx); err == nil {
		t.Fatal("unit tests have no hub child to spawn")
	}
	picked := plan.Transport.ManagedProxy.Port
	if picked == 0 {
		t.Fatal("start did not persist a hub port")
	}
	cancel()
	for i := len(actx.undo) - 1; i >= 0; i-- {
		if err := actx.undo[i](); err != nil {
			t.Fatalf("undo: %v", err)
		}
	}
	cur, err := env.Deps.Hub.GetConfigContext(context.Background(), "h")
	if err != nil || cur == nil {
		t.Fatalf("hub after undo: %+v %v", cur, err)
	}
	if cur.Port != 0 {
		t.Fatalf("port after cancelled undo = %d, want 0", cur.Port)
	}
}

// ADR 0008: the backup manifest must be durable before client config is
// written. A journal update failure is that prepare failing, not a warning.
func TestPrepareClientRecoveryReturnsJournalUpdateError(t *testing.T) {
	env := newTestEnv(t)
	actx := &activationContext{journalID: "missing-journal"}
	_, err := prepareClientRecoveryContext(context.Background(), "codex", env.Deps, actx)
	if err == nil {
		t.Fatal("a failed journal update must fail recovery prepare")
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "NOT_FOUND" {
		t.Fatalf("got %v", err)
	}
}
