package hub

import (
	"context"
	"testing"
)

// A provider-level choice pins the provider id itself; the store resolves it
// to the first attached source of that provider, so the Choose menu can offer
// "OpenAI" instead of one row per account.
func TestSetModelOwnerAcceptsBareProviderID(t *testing.T) {
	st := newHubStore(t)
	seedHubAccounts(t, st, "glm/cn", "deepseek/work")
	ctx := context.Background()
	enabled := true
	if _, err := st.UpsertConfigContext(ctx, Config{
		Name: "default", Enabled: true,
		Sources: []Source{
			{ProviderID: "glm", Account: "cn", Enabled: &enabled},
			{ProviderID: "deepseek", Account: "work", Enabled: &enabled},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetModelOwnerContext(ctx, "default", "m1", "deepseek"); err != nil {
		t.Fatalf("bare provider owner: %v", err)
	}
	route, err := st.routeByID(ctx, DefaultRouteID("default"))
	if err != nil || route == nil {
		t.Fatalf("route: %v", route)
	}
	if len(route.Models) != 1 || route.Models[0].Model != "m1" {
		t.Fatalf("models = %+v", route.Models)
	}
	cfg, err := st.GetConfigContext(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	idx := ResolveOwnerIndex(cfg.Sources, route.Models[0])
	if idx < 0 || cfg.Sources[idx].ProviderID != "deepseek" {
		t.Fatalf("pinned source = %+v", cfg.Sources)
	}
	if err := st.SetModelOwnerContext(ctx, "default", "m1", "nope"); err == nil {
		t.Fatal("unknown provider must fail")
	}
}
