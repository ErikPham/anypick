package accounts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// CachedAccount is the display read and deliberately does not rebuild the
// snapshot cache. `use` needs the real files — provider.Restore reads them — so
// it must keep resolving through Get, which re-materializes from the committed
// rows. A missing on-disk snapshot directory is a lost cache, never a lost
// account.
func TestUseRestoresSnapshotFromDatabaseAfterCacheDeleted(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	seedGrokLive(t, "cache@grok.xai")
	if _, err := svc.Save(ctx, "grok", "work", false); err != nil {
		t.Fatal(err)
	}
	saved, err := st.CachedAccount(ctx, "grok", "work")
	if err != nil || saved == nil {
		t.Fatalf("CachedAccount: %+v %v", saved, err)
	}
	if err := os.RemoveAll(saved.SnapshotDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved.SnapshotDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir still present: %v", err)
	}
	// The live login differs, so a switch that silently did nothing would show
	// up as the wrong identity rather than passing.
	seedGrokLive(t, "transient@grok.xai")

	if _, err := svc.Use(ctx, "grok", "work", UseOptions{NoRefresh: true}); err != nil {
		t.Fatalf("Use after cache delete: %v", err)
	}
	if active, _ := st.GetActive(ctx, "grok"); active != "work" {
		t.Fatalf("active = %q, want work", active)
	}
	raw, err := os.ReadFile(filepath.Join(saved.SnapshotDir, "auth.json"))
	if err != nil {
		t.Fatalf("use did not re-materialize the snapshot: %v", err)
	}
	if !contains(string(raw), "cache@grok.xai") {
		t.Fatalf("restored snapshot = %s, want the saved account", raw)
	}
}
