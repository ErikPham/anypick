package desktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/hub"
)

func hubTestEnv(t *testing.T) *trayEnv {
	t.Helper()
	env := newTrayEnv(t)
	env.Deps.Hub = &hub.Store{DB: env.Deps.Store.DB.DB, Root: env.Root}
	return env
}

func toggleHub(t *testing.T, srv *Server, reqID, hubName, provider, account string, enabled bool) Result {
	t.Helper()
	raw, err := json.Marshal(Mutate{
		Version: 1, RequestID: reqID, Operation: "hub-source-toggle",
		Name: hubName, ProviderID: provider, SourceID: account, Enabled: enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	srv.Out = &out
	if err := srv.handleMutate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	return decodeMutateResult(t, out.String())
}

// The toggle honors the explicit Enabled flag and creates the hub on first
// enable (H2): no presence guessing, no HUB_NOT_FOUND for new hubs.
func TestHubSourceToggleHonorsEnabled(t *testing.T) {
	env := hubTestEnv(t)
	ctx := context.Background()
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})

	if res := toggleHub(t, srv, "h-off-1", "default", "codex", "a", false); res.Status != "success" {
		t.Fatalf("disable on missing hub = %+v, want success no-op", res)
	}
	if res := toggleHub(t, srv, "h-on-1", "default", "codex", "a", true); res.Status != "success" {
		t.Fatalf("enable = %+v", res)
	}
	cfg, err := env.Deps.Hub.GetConfigContext(ctx, "default")
	if err != nil || cfg == nil || len(cfg.Sources) != 1 || !cfg.Sources[0].IsEnabled() {
		t.Fatalf("config after enable = %+v, %v", cfg, err)
	}
	// Idempotent re-enable, then detach.
	if res := toggleHub(t, srv, "h-on-2", "default", "codex", "a", true); res.Status != "success" {
		t.Fatalf("re-enable = %+v", res)
	}
	cfg, _ = env.Deps.Hub.GetConfigContext(ctx, "default")
	if len(cfg.Sources) != 1 {
		t.Fatalf("re-enable duplicated sources: %+v", cfg.Sources)
	}
	if res := toggleHub(t, srv, "h-off-2", "default", "codex", "a", false); res.Status != "success" {
		t.Fatalf("disable = %+v", res)
	}
	cfg, _ = env.Deps.Hub.GetConfigContext(ctx, "default")
	if len(cfg.Sources) != 0 {
		t.Fatalf("sources after disable = %+v", cfg.Sources)
	}
}

// Snapshot carries hub sources for every saved account, one lifecycle row
// for the default hub, and a hub log source (H1/H3).
func TestSnapshotEmitsHubSections(t *testing.T) {
	env := hubTestEnv(t)
	ctx := context.Background()
	env.writeCodexLive("hub@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "work", true); err != nil {
		t.Fatal(err)
	}

	snap, err := New(env.Deps, strings.NewReader(""), &strings.Builder{}).buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundSource := false
	for _, row := range snap.HubSources {
		id, _ := row["id"].(string)
		if id == "hub/default/codex/work" {
			foundSource = true
			if row["enabled"] != false || row["hub"] != "default" || row["name"] != "work" {
				t.Fatalf("source row = %+v", row)
			}
		}
	}
	if !foundSource {
		t.Fatalf("hub source missing: %+v", snap.HubSources)
	}
	foundProxy, foundLogs := false, false
	for _, p := range snap.Proxies {
		if p.ID == "proxy-hub/default" {
			foundProxy = true
			if p.ToggleActionID == "" || p.RestartActionID == "" {
				t.Fatalf("hub proxy has no actions: %+v", p)
			}
			if p.Running || p.Enabled {
				t.Fatalf("fresh hub should be stopped/disabled: %+v", p)
			}
		}
	}
	for _, l := range snap.LogSources {
		if l.ID == "proxy-hub/default" {
			foundLogs = true
		}
	}
	if !foundProxy || !foundLogs {
		t.Fatalf("hub proxy=%v logs=%v", foundProxy, foundLogs)
	}
	if len(snap.HubConflicts) != 0 {
		t.Fatalf("conflicts must stay empty without catalogs: %+v", snap.HubConflicts)
	}
}

// Pinned route models surface as published models with owners.
func TestSnapshotPublishesPinnedRouteModels(t *testing.T) {
	env := hubTestEnv(t)
	ctx := context.Background()
	env.writeCodexLive("pub@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "work", true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{
		Name: "default", Sources: []hub.Source{{ProviderID: "codex", Account: "work"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Hub.AttachRouteContext(ctx, hub.Route{
		RouteID: hub.DefaultRouteID("default"), HubName: "default",
		ClientID: "codex", Token: "tok-123",
		Models: []hub.ModelRoute{{Model: "gpt-x", SourceIndex: 0}},
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := New(env.Deps, strings.NewReader(""), &strings.Builder{}).buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.HubPublishedModels) != 1 {
		t.Fatalf("published = %+v", snap.HubPublishedModels)
	}
	row := snap.HubPublishedModels[0]
	if row["id"] != "gpt-x" || row["ownerProviderId"] != "codex" || row["needsChoice"] != false {
		t.Fatalf("published row = %+v", row)
	}
}
