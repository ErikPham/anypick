package activation

// Regression coverage for the P1 hub-activation findings:
//   - the hub/<name> scope is held for the ENTIRE critical section, so undo
//     closures touching hub state run under the coordinator (invariant #5);
//   - rollback restores an overwritten route row EXACTLY (manifest + token
//     hash + timestamps), never a plain detach;
//   - a spawned child has registered compensation before later steps run.

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/refs"
)

func TestMutationScopesHoldHubScopeForHubActivations(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: "main", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.Ref{Kind: refs.KindProxyHub, Name: "main"}),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan hub activation: %v", err)
	}
	found := false
	for _, s := range mutationScopes(plan) {
		if s == "hub/main" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hub scope missing from activation scopes %v — rollback would run outside it", mutationScopes(plan))
	}
}

func TestHubRouteRollbackRestoresExactPriorRow(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	const hubName = "main"
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: hubName, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	routeID := hubName + "/route-claude"

	// A route the user already had: attach with its own token so the stored
	// token_secret differs from anything this activation will generate.
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

	// Plan a hub activation; execution fails at SpawnHubChild (unit stub),
	// i.e. AFTER the route overwrite — forcing the compensation to run.
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

	after, err := env.Deps.Hub.SnapshotRouteContext(ctx, routeID)
	if err != nil || after == nil {
		t.Fatalf("route vanished after rollback: %v", err)
	}
	if after.ManifestJSON != before.ManifestJSON ||
		after.TokenSecret != before.TokenSecret ||
		after.CreatedAt != before.CreatedAt {
		t.Fatalf("rollback did not restore the exact prior row:\nwant %+v\ngot  %+v", before, after)
	}
}

func TestHubRouteRollbackDetachesWhenNoneExisted(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	const hubName = "fresh"
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: hubName, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	routeID := hubName + "/route-codex"

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "codex",
		Source: refPtr(refs.Ref{Kind: refs.KindProxyHub, Name: hubName}),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := Execute(ctx, plan, env.Deps, ExecuteOptions{}); err == nil {
		t.Fatal("expected activation failure at spawn step")
	}
	snap, err := env.Deps.Hub.SnapshotRouteContext(ctx, routeID)
	if err != nil {
		t.Fatal(err)
	}
	if snap != nil {
		t.Fatalf("rollback left a route behind that no prior activation owned: %+v", snap)
	}
}
