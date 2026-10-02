package desktop

import (
	"context"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/proxy"
)

func TestRestartRunningPoolsRestartsOnlyPoolLeases(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	poolStore := &pools.Store{DB: env.Deps.Store.DB.DB}
	env.Deps.Proxy.Pools = poolStore
	t.Cleanup(env.Deps.Proxy.Close)

	for _, seed := range []struct{ provider, name string }{
		{"deepseek", "pool-account"},
		{"openrouter", "account-only"},
	} {
		if _, err := env.Svc.SaveInput(ctx, seed.provider, seed.name, accounts.CredentialInput{
			Kind: "api-key", Secret: "sk-" + seed.name, Profile: "global",
		}, true); err != nil {
			t.Fatalf("seed %s/%s: %v", seed.provider, seed.name, err)
		}
	}

	poolPort, err := proxy.PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if err := poolStore.Set(ctx, pools.Pool{
		Provider: "deepseek", Mode: "multi", Enabled: true,
		Strategy: "round-robin", Host: "127.0.0.1", Port: poolPort,
		Members: []pools.Member{{Account: "pool-account", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.Proxy.StartPoolProxy(ctx, "deepseek"); err != nil {
		t.Fatalf("start pool proxy: %v", err)
	}
	poolBefore := requireOnePoolLease(t, env.Deps.Proxy, "deepseek")

	accountPort, err := proxy.PickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.Proxy.Configure(ctx, "openrouter", "account-only", true, accountPort); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Deps.Proxy.StartProxy(ctx, "openrouter", "account-only"); err != nil {
		t.Fatalf("start account proxy: %v", err)
	}
	accountBefore := requireOneAccountLease(t, env.Deps.Proxy, "openrouter", "account-only")

	// A configured multi pool without a NULL-account lease is deliberately a
	// decoy: its running account proxy must not be mistaken for a pool proxy.
	if err := poolStore.Set(ctx, pools.Pool{
		Provider: "openrouter", Mode: "multi", Enabled: true,
		Strategy: "failover", Host: "127.0.0.1", Port: accountPort,
		Members: []pools.Member{{Account: "account-only", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}

	srv := New(env.Deps, nil, nil)
	if err := srv.restartRunningPools(ctx); err != nil {
		t.Fatalf("restart running pools: %v", err)
	}

	poolAfter := requireOnePoolLease(t, env.Deps.Proxy, "deepseek")
	if poolAfter.InstanceID == poolBefore.InstanceID {
		t.Fatalf("live pool was not restarted: instance %q", poolAfter.InstanceID)
	}
	accountAfter := requireOneAccountLease(t, env.Deps.Proxy, "openrouter", "account-only")
	if accountAfter.InstanceID != accountBefore.InstanceID {
		t.Fatalf("account proxy was restarted as a pool: %q -> %q",
			accountBefore.InstanceID, accountAfter.InstanceID)
	}
}

func TestRestartRunningPoolsReturnsInspectionErrors(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.Proxy.Pools = &pools.Store{DB: env.Deps.Store.DB.DB}
	if err := env.Deps.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, nil, nil)
	if err := srv.restartRunningPools(context.Background()); err == nil {
		t.Fatal("closed pool store error was silently discarded")
	}
}

func requireOnePoolLease(t *testing.T, svc *proxy.Service, providerID string) *proxy.Lease {
	t.Helper()
	leases, err := svc.Jobs.ListPoolLeasesContext(context.Background(), providerID)
	if err != nil || len(leases) != 1 {
		t.Fatalf("pool leases for %s = %#v, %v; want one", providerID, leases, err)
	}
	return leases[0]
}

func requireOneAccountLease(t *testing.T, svc *proxy.Service, providerID, account string) *proxy.Lease {
	t.Helper()
	leases, err := svc.Jobs.ListByAccountContext(context.Background(), providerID, account)
	if err != nil || len(leases) != 1 {
		t.Fatalf("account leases for %s/%s = %#v, %v; want one",
			providerID, account, leases, err)
	}
	return leases[0]
}
