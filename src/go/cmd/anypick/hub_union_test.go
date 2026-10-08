package main

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/hub"
)

// TestHubModelsArePinsLeadingUnion pins the shape 6f2c8dc introduced: a hub
// with route pins advertises its pins FIRST, in pin order, followed by every
// model its enabled sources serve. Pins used to replace the rest of the
// surface entirely, while candidateChain still served every same-provider
// source for an unpinned model — so `models` understated what the hub accepts,
// on this install advertising 7 while its accounts publish 23.
//
// The source side runs with live=false through globals.DryRun so the test
// makes no vendor egress; the union contract is about ordering and presence,
// not discovery.
func TestHubModelsArePinsLeadingUnion(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	if code := run([]string{"add", "deepseek", "--name", "ds", "--kind", "api-key", "--secret", "sk-ds"}); code != 0 {
		t.Fatalf("add exited %d", code)
	}
	if code := run([]string{"proxy", "hub", "enable", "test"}); code != 0 {
		t.Fatalf("hub enable exited %d", code)
	}
	if code := run([]string{"proxy", "hub", "source", "test", "deepseek", "ds"}); code != 0 {
		t.Fatalf("hub source exited %d", code)
	}

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	// Two pins, deliberately out of catalog order, plus an owner-free
	// placeholder pin to exercise trimming.
	// Pins live in a hub route's manifest, not the hub config. AttachRoute is
	// the write path; the blank model exercises the trimming rule.
	if aerr := a.ProxyHub.AttachRouteContext(context.Background(), hub.Route{
		RouteID: hub.DefaultRouteID("test"), HubName: "test", ClientID: "claude",
		Models: []hub.ModelRoute{
			{Model: "zz-pinned-a", SourceIndex: 0},
			{Model: "  ", SourceIndex: 0},
			{Model: "aa-pinned-b", SourceIndex: 0},
		},
	}); aerr != nil {
		t.Fatalf("attach route: %v", aerr)
	}

	globals.DryRun = true
	defer func() { globals.DryRun = false }()
	models, merr := hubActivationModels(context.Background(), a, "test")
	if merr != nil {
		t.Fatal(merr)
	}

	if len(models) < 2 || models[0] != "zz-pinned-a" || models[1] != "aa-pinned-b" {
		t.Fatalf("pins must lead in pin order: %v", models)
	}
	if !containsString(models, "deepseek-chat") {
		t.Fatalf("union missing the source's static catalog after the pins: %v", models)
	}
	seen := map[string]bool{}
	for _, m := range models {
		if seen[m] {
			t.Fatalf("duplicate model %q in the union: %v", m, models)
		}
		seen[m] = true
	}
}
