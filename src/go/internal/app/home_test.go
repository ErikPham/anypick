package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The composition root must scope live credential reads to the injected client
// home. A temp home carrying a fake Claude login is the home the registered
// provider and client resolve; only the sentinel planted below can satisfy the
// assertions, so a registry built against the real home fails them.
func TestReadyUsesInjectedHomeForLiveReads(t *testing.T) {
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	home := t.TempDir()
	root := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"),
		[]byte(`{"email":"seam-probe@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:9"},`+
			`"_anypickManaged":{"keys":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a, closeFn, err := Ready(context.Background(), Options{
		Root: root, Home: home, SkipMigrate: true, SkipMaintenance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	if a.Home != home {
		t.Fatalf("a.Home = %q, want %q", a.Home, home)
	}
	if a.Runtime.Home != home {
		t.Fatalf("a.Runtime.Home = %q, want %q", a.Runtime.Home, home)
	}
	claudeProv, err := a.Providers.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	status, err := claudeProv.DetectLive()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Present || status.Identity != "seam-probe@example.com" {
		t.Fatalf("claude DetectLive = %+v, want the temp-home login", status)
	}
	claudeClient, err := a.Clients.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := claudeClient.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !inspect.Present {
		t.Fatal("claude Inspect must see the temp-home managed settings")
	}
}
