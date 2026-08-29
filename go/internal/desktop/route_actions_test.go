package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/activation"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/presets"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/runtime"
)

// Route actions (Switch-menu chips, app-route chips) must activate through the
// persistent planner/executor — the bindingService.use port. TS authority:
// src/tray/supervisor-actions.ts invokeTrayAction routes EVERY non-proxy
// target through app.bindingService.use(target.clientId, { with: target.source }).
// A handler that mutates the source provider's account directly (wrong scope)
// or returns success without mutating anything (false success) violates that
// contract.

// wireActivation mirrors internal/app compose so desktop handlers exercise the
// same planner/executor graph the CLI uses.
func wireActivation(t *testing.T, env *trayEnv) {
	t.Helper()
	database := env.Deps.Store.DB.DB
	rt := &runtime.Service{
		Root:     env.Root,
		Home:     env.Home,
		Clients:  env.Deps.Clients,
		Profiles: env.Deps.Profiles,
		Catalog:  catalog.NewIndex(catalog.Builtin()),
	}
	catalogEntries := map[string]activation.CatalogEntry{}
	for id, p := range catalog.Builtin() {
		catalogEntries[id] = activation.CatalogEntry{Name: p.Name, APIStyle: string(p.APIStyle), Protocols: p.Protocols}
	}
	env.Deps.Hub = &hub.Store{DB: database}
	env.Deps.Activation = &activation.Deps{
		Root:      env.Root,
		Accounts:  activation.NewAccountsView(env.Svc),
		Providers: env.Deps.Providers,
		Clients:   env.Deps.Clients,
		Bindings:  env.Deps.Bindings,
		Presets:   activation.NewPresetsView(&presets.Store{DB: database}),
		Profiles:  rt.Profiles,
		Pools:     activation.NewPoolsView(&pools.Store{DB: database}),
		Journal:   &journal.OperationJournal{DB: database},
		Runtime:   rt,
		Proxy:     env.Deps.Proxy,
		Catalog:   activation.NewCatalogIndex(catalogEntries),
		Hub:       env.Deps.Hub,
	}
}

func invokeAndWait(t *testing.T, inW *os.File, outR *os.File, rev int, actionID, reqID string) map[string]interface{} {
	t.Helper()
	sendCommand(t, inW, "invoke\t"+encodePayload(map[string]interface{}{
		"version": 1, "requestId": reqID, "revision": rev, "actionId": actionID,
	}))
	for {
		frame := readFrame(t, outR, "")
		if strings.HasPrefix(frame, "result\t") {
			res := decodeJSON(t, frame[len("result\t"):])
			if res["requestId"] == reqID {
				return res
			}
		}
	}
}

func findAction(snap map[string]interface{}, pred func(map[string]interface{}) bool) string {
	rows, _ := snap["actions"].([]interface{})
	for _, raw := range rows {
		row := raw.(map[string]interface{})
		if pred(row) {
			id, _ := row["id"].(string)
			return id
		}
	}
	return ""
}

func stopServer(inW *os.File) {
	_ = inW.Close() // scanner EOF → Run shuts down gracefully
}

// writeGrokLive points GROK_AUTH_PATH at a temp auth file with one session.
func writeGrokLive(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", path)
	data := `{"session":{"email":"grok-a@example.com","token":"tok-grok-a"}}`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A gateway app-route chip must write a global binding for the client via the
// planner/executor — not report success while doing nothing.
func TestInvokeGatewayRouteActionWritesBinding(t *testing.T) {
	env := newTrayEnv(t)
	wireActivation(t, env)
	ctx := context.Background()

	if _, err := env.Deps.Profiles.UpsertContext(ctx, profile.Runtime{
		Meta:    profile.Meta{Name: "gw1", Provider: "deepseek", Endpoint: "https://api.deepseek.com"},
		Secrets: profile.Secrets{APIKey: "sk-test"},
	}); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, inR, outW)
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(func() { stopServer(inW) })

	first := readFrame(t, outR, "snapshot")
	snap := decodeJSON(t, first)
	rev := int(snap["revision"].(float64))

	actionID := findAction(snap, func(row map[string]interface{}) bool {
		kind, _ := row["kind"].(string)
		src, _ := row["sourceId"].(string)
		client, _ := row["clientId"].(string)
		return kind == "gateway" && src == "gateway/gw1" && client == "codex"
	})
	if actionID == "" {
		t.Fatal("gateway route action missing from snapshot")
	}

	res := invokeAndWait(t, inW, outR, rev, actionID, "gw-1")
	if res["status"] != "success" {
		t.Fatalf("gateway route invoke failed: %v", res["message"])
	}

	binding, err := env.Deps.Bindings.GetGlobal("codex")
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		t.Fatal("gateway chip reported success but wrote no global binding for codex")
	}
	if got := refs.SerializeRef(binding.Spec.Source); got != "gateway/gw1" {
		t.Fatalf("binding source = %q, want gateway/gw1", got)
	}
}

// A cross-provider app-route chip must bind the CLIENT to the source account.
// Mutating the source provider's active account instead is a wrong-scope
// mutation: clicking "Claude Code ← grok/a" must not touch grok's live auth.
func TestInvokeCrossProviderAppRouteBindsClientNotSourceProvider(t *testing.T) {
	env := newTrayEnv(t)
	wireActivation(t, env)
	grokAuth := writeGrokLive(t)
	ctx := context.Background()

	if _, err := env.Svc.Save(ctx, "grok", "a", true); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, inR, outW)
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(func() { stopServer(inW) })

	first := readFrame(t, outR, "snapshot")
	snap := decodeJSON(t, first)
	rev := int(snap["revision"].(float64))

	actionID := findAction(snap, func(row map[string]interface{}) bool {
		client, _ := row["clientId"].(string)
		src, _ := row["sourceId"].(string)
		return client == "claude" && src == "account/grok/a"
	})
	if actionID == "" {
		t.Fatal("cross-provider app-route action for claude × grok/a missing")
	}

	res := invokeAndWait(t, inW, outR, rev, actionID, "xprov-1")
	if res["status"] != "success" {
		t.Fatalf("cross-provider route invoke failed: %v", res["message"])
	}

	binding, err := env.Deps.Bindings.GetGlobal("claude")
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		t.Fatal("app-route chip reported success but wrote no global binding for claude")
	}
	if got := refs.SerializeRef(binding.Spec.Source); got != "account/grok/a" {
		t.Fatalf("binding source = %q, want account/grok/a", got)
	}

	// The source provider's live login must be untouched. Grok's adapter
	// declares requiresNativeAuthWrite: false (src/sources/account-adapters.ts),
	// so TS's WriteNativeAuth step never runs for grok sources and switching
	// claude onto grok/a must not rewrite grok's live auth or active pointer.
	live, rerr := os.ReadFile(grokAuth)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(live), "grok-a@example.com") {
		t.Fatalf("grok live auth was mutated by a claude route switch: %s", live)
	}
	if active, _ := env.Deps.Store.GetActive("grok"); active != "" {
		t.Fatalf("grok active pointer = %q, want \"\" (route switch must not switch the source provider)", active)
	}
}

// Clicking the already-selected current-route row re-applies the bound source
// through the same planned path; it must never be a silent no-op.
func TestInvokeCurrentRouteReappliesBinding(t *testing.T) {
	env := newTrayEnv(t)
	wireActivation(t, env)
	ctx := context.Background()

	env.writeCodexLive("a@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.Bindings.UpsertGlobal(bindings.Global{
		Client: "codex",
		Spec: bindings.Spec{
			Client: "codex",
			Source: refs.AccountRef("codex", "a"),
			Model:  bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, inR, outW)
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(func() { stopServer(inW) })

	first := readFrame(t, outR, "snapshot")
	snap := decodeJSON(t, first)
	rev := int(snap["revision"].(float64))

	actionID := findAction(snap, func(row map[string]interface{}) bool {
		selected, _ := row["selected"].(bool)
		client, _ := row["clientId"].(string)
		pres, _ := row["presentation"].(string)
		route, _ := row["routeKind"].(string)
		return selected && client == "codex" && pres == "app-route" && route == "direct-account"
	})
	if actionID == "" {
		t.Fatal("selected current-route action missing for codex")
	}

	res := invokeAndWait(t, inW, outR, rev, actionID, "cur-1")
	if res["status"] != "success" {
		t.Fatalf("current-route invoke failed: %v", res["message"])
	}
	binding, err := env.Deps.Bindings.GetGlobal("codex")
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || refs.SerializeRef(binding.Spec.Source) != "account/codex/a" {
		t.Fatal("current-route re-invoke lost or rewrote the codex binding")
	}
}
