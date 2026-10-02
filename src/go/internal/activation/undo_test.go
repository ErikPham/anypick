package activation

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
)

// A kill after the hub row changes leaves the journal executing and the
// in-memory undo stack gone. Recovery must put the prior route and port back.
func TestCrashRecoveryRestoresHubRouteAndPort(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	const hubName = "main"
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: hubName, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	routeID := hubName + "/route-claude"
	prevToken := "token-PREVIOUS-USER-ROUTE"
	if err := env.Deps.Hub.AttachRouteContext(ctx, hub.Route{
		RouteID: routeID, HubName: hubName, ClientID: "claude",
		Models: []hub.ModelRoute{{Model: "user-model", SourceIndex: 0}},
		Token:  prevToken,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := env.Deps.Hub.SnapshotRouteContext(ctx, routeID)
	if err != nil || before == nil {
		t.Fatalf("snapshot pre-route: %v", err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.Ref{Kind: refs.KindProxyHub, Name: hubName}),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := Execute(ctx, plan, env.Deps, ExecuteOptions{}); err == nil {
		t.Fatal("expected activation failure at spawn step")
	}

	recent, err := env.Deps.Journal.ListRecent(5)
	if err != nil {
		t.Fatal(err)
	}
	var recorded journal.Entry
	found := false
	for _, entry := range recent {
		if entry.Params["activationUndo"] != nil {
			recorded = entry
			found = true
			break
		}
	}
	if !found {
		t.Fatal("hub activation did not journal a compensation")
	}
	records, ok := recorded.Params["activationUndo"].([]any)
	if !ok || len(records) == 0 {
		t.Fatalf("activationUndo = %#v", recorded.Params["activationUndo"])
	}
	var picked int
	sawRoute, sawPort := false, false
	for _, item := range records {
		rec, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("record type %T", item)
		}
		switch rec["kind"] {
		case "hub-route":
			sawRoute = true
			snap, _ := rec["snapshot"].(map[string]any)
			if snap["manifestJSON"] != before.ManifestJSON || snap["tokenSecret"] != before.TokenSecret {
				t.Fatalf("journalled route is not the prior row: %#v", snap)
			}
		case "hub-port":
			sawPort = true
			var okPort bool
			picked, okPort = undoInt(rec["pickedPort"])
			prev, okPrev := undoInt(rec["previousPort"])
			if !okPort || !okPrev || prev != 0 || picked == 0 {
				t.Fatalf("hub-port record = %#v", rec)
			}
		}
	}
	if !sawRoute || !sawPort {
		t.Fatalf("journal records missing route or port: %#v", records)
	}

	// Crash window: the new port and route are on disk, and the undo stack is gone.
	cfg, err := env.Deps.Hub.GetConfigContext(ctx, hubName)
	if err != nil || cfg == nil {
		t.Fatal(err)
	}
	cfg.Port = picked
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Hub.AttachRouteContext(ctx, hub.Route{
		RouteID: routeID, HubName: hubName, ClientID: "claude", Token: "token-CRASH-WINDOW",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.Journal.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		State:  journal.StateExecuting,
		Params: map[string]any{"activationUndo": recorded.Params["activationUndo"]},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := RecoverIncompleteOperations(ctx, env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || len(res.Failed) != 0 {
		t.Fatalf("recovery result = %+v", res)
	}
	after, err := env.Deps.Hub.SnapshotRouteContext(ctx, routeID)
	if err != nil || after == nil {
		t.Fatalf("route after recovery: %v", err)
	}
	if after.ManifestJSON != before.ManifestJSON || after.TokenSecret != before.TokenSecret ||
		after.CreatedAt != before.CreatedAt {
		t.Fatalf("recovery did not restore the exact prior row:\nwant %+v\ngot  %+v", before, after)
	}
	restoredCfg, err := env.Deps.Hub.GetConfigContext(ctx, hubName)
	if err != nil || restoredCfg == nil || restoredCfg.Port != 0 {
		t.Fatalf("port after recovery = %+v, err %v", restoredCfg, err)
	}
}

func TestCrashRecoveryRestoresPreviousGlobalBinding(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	previous := bindings.Global{
		Client:     "codex",
		Spec:       bindings.Spec{Client: "codex", TransportPolicy: "proxy"},
		Provenance: bindings.Provenance{Kind: "legacy_migration"},
	}
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, previous); err != nil {
		t.Fatal(err)
	}
	entry, err := env.Deps.Journal.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		State: journal.StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}
	actx := &activationContext{journalID: entry.ID}
	if err := commitGlobalBinding(ctx, &Plan{
		Mode:        ModePersistent,
		Client:      "codex",
		BindingSpec: &bindings.Spec{Client: "codex", TransportPolicy: "direct"},
		Provenance:  &bindings.Provenance{Kind: "direct"},
	}, env.Deps, actx); err != nil {
		t.Fatal(err)
	}
	// The in-memory undo is not run. That is the crash.
	res, err := RecoverIncompleteOperations(ctx, env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || len(res.Failed) != 0 {
		t.Fatalf("recovery result = %+v", res)
	}
	restored, err := env.Deps.Bindings.GetGlobalContext(ctx, "codex")
	if err != nil || restored == nil || restored.Spec.TransportPolicy != "proxy" ||
		restored.Provenance.Kind != "legacy_migration" {
		t.Fatalf("previous binding not restored: %+v %v", restored, err)
	}
}

func TestCrashRecoveryDeletesBindingThatDidNotExist(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	root := t.TempDir()

	entry, err := env.Deps.Journal.CreateContext(ctx, "activate:project", journal.CreateOptions{
		State: journal.StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}
	actx := &activationContext{journalID: entry.ID}
	if err := commitProjectBinding(ctx, PlanStep{}, &Plan{
		Mode:        ModeProject,
		Client:      "codex",
		BindingSpec: &bindings.Spec{Client: "codex", TransportPolicy: "direct"},
		Provenance:  &bindings.Provenance{Kind: "direct"},
	}, env.Deps, ExecuteOptions{ProjectRoot: root}, actx); err != nil {
		t.Fatal(err)
	}
	if stored, _ := env.Deps.Bindings.GetProjectContext(ctx, root, "codex"); stored == nil {
		t.Fatal("project binding was not committed")
	}
	res, err := RecoverIncompleteOperations(ctx, env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || len(res.Failed) != 0 {
		t.Fatalf("recovery result = %+v", res)
	}
	if stored, err := env.Deps.Bindings.GetProjectContext(ctx, root, "codex"); err != nil || stored != nil {
		t.Fatalf("crash recovery left the new project binding: %+v %v", stored, err)
	}
}

func TestCrashRecoveryRestoresPreviousClientProfile(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	previous := bindings.Spec{Client: "codex", TransportPolicy: "proxy"}
	if _, err := env.Deps.ClientProfiles.UpsertContext(ctx, "codex", "work", previous); err != nil {
		t.Fatal(err)
	}
	entry, err := env.Deps.Journal.CreateContext(ctx, "activate:profile", journal.CreateOptions{
		State: journal.StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}
	actx := &activationContext{journalID: entry.ID}
	if err := commitClientProfile(ctx, &Plan{
		Mode:        ModeProfile,
		Client:      "codex",
		ProfileName: "work",
		BindingSpec: &bindings.Spec{Client: "codex", TransportPolicy: "direct"},
	}, env.Deps, actx); err != nil {
		t.Fatal(err)
	}
	res, err := RecoverIncompleteOperations(ctx, env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || len(res.Failed) != 0 {
		t.Fatalf("recovery result = %+v", res)
	}
	restored, err := env.Deps.ClientProfiles.GetContext(ctx, "codex", "work")
	if err != nil || restored == nil || restored.Spec.TransportPolicy != "proxy" {
		t.Fatalf("previous profile not restored: %+v %v", restored, err)
	}
}
