package activation

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/refs"
)

func providerDefaultsDeps(t *testing.T) *Deps {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &Deps{Root: root, Bindings: &bindings.Store{Root: root, DB: database.DB}}
}

// Stored provider defaults fill roles the connection left unset, filtered to
// roles the client declares and models its catalog serves. Anything else
// stays engine default: a provider-level model the new entitlement cannot
// serve must never be written into client config.
func TestProviderModelRoleDefaults(t *testing.T) {
	deps := providerDefaultsDeps(t)
	ctx := context.Background()
	source := refs.AccountRef("claude", "work")
	if _, ok := providerModelRoleDefaults(ctx, deps, "claude", source, []string{"opus", "sonnet"}); ok {
		t.Fatal("no stored defaults must copy nothing")
	}
	if err := deps.Bindings.SetProviderModelRoles(ctx, "claude",
		map[string]string{"default": "opus", "sonnet": "sonnet", "opus": "opus-x", "bogus": "m"}); err != nil {
		t.Fatal(err)
	}
	got, ok := providerModelRoleDefaults(ctx, deps, "claude", source, []string{"opus", "sonnet"})
	if !ok {
		t.Fatal("stored defaults must copy")
	}
	// "bogus" is not a claude role; "opus-x" (opus slot) is not in the
	// catalog. Both drop; the rest applies.
	if len(got) != 2 || got["default"] != "opus" || got["sonnet"] != "sonnet" {
		t.Fatalf("filtered copy = %+v", got)
	}
	// Gateway and hub sources key by their own identity, never the vendor.
	if _, ok := providerModelRoleDefaults(ctx, deps, "codex",
		refs.GatewayRef("Production"), []string{"m"}); ok {
		t.Fatal("gateway bucket is separate from provider buckets")
	}
	if err := deps.Bindings.SetProviderModelRoles(ctx, "gateway/Production",
		map[string]string{"default": "m"}); err != nil {
		t.Fatal(err)
	}
	got, ok = providerModelRoleDefaults(ctx, deps, "codex",
		refs.GatewayRef("Production"), []string{"m"})
	if !ok || got["default"] != "m" {
		t.Fatalf("gateway bucket = %+v, %v", got, ok)
	}
	// Unknown sources and nil compositions copy nothing instead of failing.
	if _, ok := providerModelRoleDefaults(ctx, deps, "claude",
		refs.PresetRef("@work"), []string{"opus"}); ok {
		t.Fatal("preset source must not resolve provider defaults")
	}
	if _, ok := providerModelRoleDefaults(ctx, nil, "claude", source, []string{"opus"}); ok {
		t.Fatal("nil deps must copy nothing")
	}
}

// A fresh binding with no explicit choice inherits stored provider defaults
// as pins (marker "provider"); an explicit choice stamps "explicit" instead.
// Legacy bindings carry no marker and keep working untouched.
func TestPlanFreshSourceCopiesProviderDefaults(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedGateway(t, env, "or-work")
	if err := env.Deps.Bindings.SetProviderModelRoles(ctx, "gateway/or-work",
		map[string]string{"default": "vendor/large", "sonnet": "other"}); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "codex",
		Source: refPtr(refs.GatewayRef("or-work")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	roles, _ := plan.BindingSpec.ClientOptions["modelRoles"].(map[string]interface{})
	if roles["default"] != "vendor/large" {
		t.Fatalf("inherited pins = %+v", roles)
	}
	if _, ok := roles["sonnet"]; ok {
		t.Fatalf("unlisted role must not copy: %+v", roles)
	}
	marker, _ := plan.BindingSpec.ClientOptions["modelRolesSource"].(string)
	if marker != "provider" {
		t.Fatalf("marker = %q, want provider", marker)
	}

	explicit, err := PlanActivation(ctx, Request{
		Mode:       ModePersistent,
		Client:     "codex",
		Source:     refPtr(refs.GatewayRef("or-work")),
		ModelRoles: map[string]string{"default": "vendor/large"},
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	marker, _ = explicit.BindingSpec.ClientOptions["modelRolesSource"].(string)
	if marker != "explicit" {
		t.Fatalf("marker = %q, want explicit", marker)
	}
}
