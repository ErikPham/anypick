package hub

import (
	"context"
	"testing"
)

// seedHubAccounts inserts minimal account rows so foreign-keyed membership
// accepts test fixtures. Tests may write any tables directly; production
// code never does (invariant #6).
func seedHubAccounts(t *testing.T, st *Store, pairs ...string) {
	t.Helper()
	for _, p := range pairs {
		provider, account := splitHubPair(p)
		if _, err := st.DB.Exec(
			`INSERT INTO accounts (provider, name, meta_json, created_at, updated_at)
			 VALUES (?, ?, '{}', 't', 't') ON CONFLICT(provider, name) DO NOTHING`,
			provider, account); err != nil {
			t.Fatal(err)
		}
	}
}

func splitHubPair(p string) (string, string) {
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			return p[:i], p[i+1:]
		}
	}
	return p, ""
}

// Removing a source drops exactly the pins that named it by value.
// Positions are never rewritten because nothing addresses them anymore:
// value pins cannot re-point at a neighbor the way SourceIndex did.
func TestRemoveSourceDropsValuePins(t *testing.T) {
	st := newHubStore(t)
	ctx := context.Background()
	seedHubAccounts(t, st, "workbuddy/ghost", "gemini/g", "workbuddy/live")
	if _, err := st.UpsertConfigContext(ctx, Config{
		Name: "h", Enabled: true,
		Sources: []Source{
			{ProviderID: "workbuddy", Account: "ghost"},
			{ProviderID: "gemini", Account: "g"},
			{ProviderID: "workbuddy", Account: "live"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	token := mustNewProxyToken(t)
	if err := st.AttachRouteContext(ctx, Route{RouteID: "default-route", HubName: "h", ClientID: "cli", Token: token,
		Models: []ModelRoute{
			{Model: "dead-model", Owner: "workbuddy/ghost", SourceIndex: -1},
			{Model: "kept-model", Owner: "workbuddy/live", SourceIndex: -1},
		}}); err != nil {
		t.Fatal(err)
	}

	removed, dropped, err := st.RemoveSourceContext(ctx, "h", "workbuddy", "ghost")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if len(dropped) != 1 || dropped[0] != "dead-model" {
		t.Fatalf("dropped = %v, want [dead-model]", dropped)
	}
	cfg, err := st.GetConfigContext(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 2 || cfg.Sources[0].Account != "g" || cfg.Sources[1].Account != "live" {
		t.Fatalf("sources = %+v", cfg.Sources)
	}
	route, err := st.routeByID(ctx, "default-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Models) != 1 || route.Models[0].Owner != "workbuddy/live" {
		t.Fatalf("pins = %+v, want only kept-model", route.Models)
	}
	if removed, _, err := st.RemoveSourceContext(ctx, "h", "workbuddy", "ghost"); err != nil || removed {
		t.Fatalf("second remove = %v, %v", removed, err)
	}
}

// Upserting a source for an account that does not exist fails closed instead
// of writing a ghost the snapshot hides and serving trips over. Pool refs
// carry no account and always pass.
func TestUpsertConfigRejectsGhostSource(t *testing.T) {
	st := newHubStore(t)
	ctx := context.Background()
	if _, err := st.UpsertConfigContext(ctx, Config{
		Name: "h", Enabled: true,
		Sources: []Source{
			{ProviderID: "workbuddy", Account: "ghost"},
		},
	}); err == nil {
		t.Fatal("upsert with ghost source succeeded; want a foreign-key refusal")
	}
	if _, err := st.UpsertConfigContext(ctx, Config{
		Name: "h", Enabled: true,
		Sources: []Source{
			{ProviderID: "codex", Account: "pool:codex"},
		},
	}); err != nil {
		t.Fatalf("pool source refused: %v", err)
	}
}

// Pruning a deleted account drops its value pins across every hub. Source
// rows need no pruning: the foreign key cascades the account delete itself.
func TestPruneAccountSweepsHubs(t *testing.T) {
	st := newHubStore(t)
	ctx := context.Background()
	seedHubAccounts(t, st, "gemini/g")
	for _, name := range []string{"a", "b"} {
		if _, err := st.UpsertConfigContext(ctx, Config{
			Name: name, Enabled: true,
			Sources: []Source{
				{ProviderID: "gemini", Account: "g"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	token := mustNewProxyToken(t)
	if err := st.AttachRouteContext(ctx, Route{RouteID: "x", HubName: "a", ClientID: "cli", Token: token,
		Models: []ModelRoute{{Model: "m", Owner: "workbuddy/gone", SourceIndex: -1}}}); err != nil {
		t.Fatal(err)
	}

	pruned, err := st.PruneAccountContext(ctx, "workbuddy", "gone")
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0] != "a: m" {
		t.Fatalf("pruned = %v, want [a: m]", pruned)
	}
	route, err := st.routeByID(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Models) != 0 {
		t.Fatalf("pins to pruned source survive: %+v", route.Models)
	}
	if pruned, err := st.PruneAccountContext(ctx, "workbuddy", "never-existed"); err != nil || len(pruned) != 0 {
		t.Fatalf("prune missing = %v, %v", pruned, err)
	}
}

// Deleting the sole member cascades the membership row. The stale document
// must not bring that source back, including after the same account name is
// saved again.
func TestDeleteLastMemberDoesNotResurrectHubSource(t *testing.T) {
	st := newHubStore(t)
	ctx := context.Background()
	seedHubAccounts(t, st, "gemini/g")
	if _, err := st.UpsertConfigContext(ctx, Config{
		Name: "h", Enabled: true,
		Sources: []Source{
			{ProviderID: "gemini", Account: "g"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`DELETE FROM accounts WHERE provider = 'gemini' AND name = 'g'`); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetConfigContext(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 0 {
		t.Fatalf("sources after delete = %+v, want none", cfg.Sources)
	}
	if _, err := st.PruneAccountContext(ctx, "gemini", "g"); err != nil {
		t.Fatal(err)
	}
	seedHubAccounts(t, st, "gemini/g")
	cfg, err = st.GetConfigContext(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 0 {
		t.Fatalf("reused account name was resurrected: %+v", cfg.Sources)
	}
}

// A document that has never had membership rows still resolves. The empty-row
// fallback stays for that shape; only a Go-written relational document trusts
// an empty row set.
func TestLegacyHubDocumentReadsWhileAccountsExist(t *testing.T) {
	st := newHubStore(t)
	ctx := context.Background()
	seedHubAccounts(t, st, "gemini/g")
	const doc = `{"name":"h","enabled":true,"host":"127.0.0.1","port":1,"sources":[{"ref":{"kind":"account","provider":"gemini","name":"g"},"enabled":true}],"modelOwners":[],"revision":1,"createdAt":"t","updatedAt":"t"}`
	if _, err := st.DB.Exec(
		`INSERT INTO proxy_hubs (name, config_json, created_at, updated_at) VALUES ('h', ?, 't', 't')`, doc); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetConfigContext(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].Account != "g" {
		t.Fatalf("legacy sources = %+v, want gemini/g", cfg.Sources)
	}
}
