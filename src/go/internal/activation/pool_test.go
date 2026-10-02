// Pool-activation executor regressions (NATIVE-M1: pool:<provider> is real
// transport): the account-pool branch starts the shared provider pool, fills
// ManagedProxy lease/token/port + Transport.Endpoint, and its undo stops the
// whole pool. The account branch must stay untouched.
package activation

import (
	"context"
	"fmt"
	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newPoolTestEnv registers a stub compatible vendor with two saved accounts
// and enables a multi pool over them. All members resolve to the same stub
// upstream through the test resolver.
func newPoolTestEnv(t *testing.T, upstreamURL string) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	ctx := context.Background()
	env.Px.Pools = env.Deps.Pools.Store

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upstream":true}`))
	}))
	t.Cleanup(upstream.Close)
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	env.UpstreamURL = parsed
	env.UpstreamAuth = "sk-upstream-secret"
	if upstreamURL != "" {
		_ = upstreamURL
	}

	def := provider.CompatibleDefinition{
		ID: "vendorx", Name: "Vendor X",
		Description: "pool fixture vendor",
		Credentials: []provider.CredentialDefinition{{
			Kind: "api-key", Label: "Vendor key",
			Profiles: []provider.EndpointProfile{{
				ID: "global", OpenAIBase: upstream.URL + "/v1",
			}},
		}},
	}
	cp, err := provider.NewCompatible(def)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Providers.Register(cp); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		// Distinct credentials per member: a pool rotates real accounts, and
		// the account service refuses to save one snapshot under two names.
		if _, err := env.Svc.SaveInput(ctx, "vendorx", name,
			accounts.CredentialInput{Kind: "api-key", Secret: env.UpstreamAuth + "-" + name, Profile: "global"},
			true); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}
	// Persist a deterministic FREE port: production binds pool.port ??
	// provider.defaultPort ?? 8080 exactly, so the fixture pins a free one to
	// stay hermetic while still exercising plan/actual parity.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Pools.Store.SetMultiContext(ctx, "vendorx",
		[]pools.Member{{Account: "a", Enabled: true}, {Account: "b", Enabled: true}},
		"127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	return env
}

func poolHealthy(t *testing.T, endpoint string) bool {
	t.Helper()
	resp, err := (&http.Client{Timeout: time.Second}).Get(strings.TrimSuffix(endpoint, "/") + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func waitPoolDown(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !poolHealthy(t, endpoint) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("pool endpoint still healthy after stop/rollback")
}

// Plan/actual port parity: the planner must advertise the effective pool port
// (pool.port ?? provider.defaultProxyPort ?? 8080) and the executor must bind
// exactly that port — the advertised endpoint never diverges from the served one.
func TestPlanAdvertisesEffectivePoolPortAndExecutorBindsIt(t *testing.T) {
	env := newPoolTestEnv(t, "")
	ctx := context.Background()

	// Explicit persisted port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err := env.Deps.Pools.GetContext(ctx, "vendorx")
	if err != nil || pool == nil {
		t.Fatalf("pool row missing: %+v err=%v", pool, err)
	}
	pool.Port = port
	if err := env.Deps.Pools.Store.Set(ctx, *pool); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode: ModePersistent, Client: "claude",
		Source: refPtr(refs.AccountPoolRef("vendorx")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	if plan.Transport.Endpoint != want || plan.Transport.ManagedProxy.Port != port {
		t.Fatalf("planned endpoint = %q port = %d, want %q",
			plan.Transport.Endpoint, plan.Transport.ManagedProxy.Port, want)
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = env.Px.StopPoolProxy(ctx, "vendorx") }()
	// The served endpoint equals the planned host:port — no silent relocation.
	if result.ProxyEndpoint != plan.Transport.Endpoint {
		t.Fatalf("served endpoint %q diverges from planned %q",
			result.ProxyEndpoint, plan.Transport.Endpoint)
	}
}

// The default branches of the pool-port decision mirror TS validatePort input:
// provider.defaultProxyPort when declared, else 8080.
func TestPlannedPoolPortDefaults(t *testing.T) {
	env := newPoolTestEnv(t, "")
	ctx := context.Background()

	// vendorx has no DefaultProxyPort → 8080 once no explicit port persists.
	pool, err := env.Deps.Pools.GetContext(ctx, "vendorx")
	if err != nil || pool == nil {
		t.Fatalf("pool row missing: %+v err=%v", pool, err)
	}
	pool.Port = 0
	if err := env.Deps.Pools.Store.Set(ctx, *pool); err != nil {
		t.Fatal(err)
	}
	got, err := plannedPoolPort(ctx, "vendorx", env.Deps)
	if err != nil || got != 8080 {
		t.Fatalf("fallback port = %d err=%v, want 8080", got, err)
	}

	// A compatible definition with defaultProxyPort wins over the fallback.
	cp, err := provider.NewCompatible(provider.CompatibleDefinition{
		ID: "porty", Name: "Porty", Description: "default-port fixture",
		DefaultProxyPort: 9123,
		Credentials: []provider.CredentialDefinition{{
			Kind: "api-key", Label: "key",
			Profiles: []provider.EndpointProfile{{ID: "global", OpenAIBase: "https://example.invalid/v1"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Providers.Register(cp); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Pools.Store.SetMultiContext(ctx, "porty",
		[]pools.Member{{Account: "a", Enabled: true}}, "127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	got, err = plannedPoolPort(ctx, "porty", env.Deps)
	if err != nil || got != 9123 {
		t.Fatalf("provider-default port = %d err=%v, want 9123", got, err)
	}
}

// Full pool activation: plan → execute fills lease/token/port + endpoint, the
// shared listener serves authenticated requests, and state lands under the
// primary member exactly like TS writeProxyState(providerId, primary, …).
func TestExecutePoolActivationFillsManagedProxy(t *testing.T) {
	env := newPoolTestEnv(t, "")
	ctx := context.Background()

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.AccountPoolRef("vendorx")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Transport.Capability != "managed_builtin_proxy" {
		t.Fatalf("capability = %s, want managed_builtin_proxy", plan.Transport.Capability)
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() {
		_ = env.Px.StopPoolProxy(ctx, "vendorx")
	}()

	mp := plan.Transport.ManagedProxy
	if mp == nil {
		t.Fatal("plan transport has no managedProxy block")
	}
	// Never fall back to the unauthenticated placeholder token.
	if mp.Token == "" || mp.Token == "anypick-proxy" {
		t.Fatalf("managedProxy.token = %q, want a real per-instance secret", mp.Token)
	}
	if mp.LeaseID == "" || mp.LeaseID == "pending" {
		t.Fatalf("managedProxy.leaseId = %q, want the created lease", mp.LeaseID)
	}
	if mp.Port == 0 || result.ProxyEndpoint == "" ||
		!strings.HasPrefix(result.ProxyEndpoint, "http://127.0.0.1:") {
		t.Fatalf("endpoint = %q port = %d", result.ProxyEndpoint, mp.Port)
	}
	if !poolHealthy(t, result.ProxyEndpoint) {
		t.Fatal("pool child is not serving the planned endpoint")
	}

	// Pool lease is keyed (provider, "") — never per-account scopes.
	leases, lerr := env.Px.Jobs.ListPoolLeases("vendorx")
	if lerr != nil || len(leases) != 1 || leases[0].LeaseID != mp.LeaseID {
		t.Fatalf("pool leases = %+v err=%v want one matching lease", leases, lerr)
	}

	// Proxy state persists under the PRIMARY member with the same token.
	state, serr := env.Px.Store.ReadProxyState(context.Background(), "vendorx", "a")
	if serr != nil || state == nil {
		t.Fatalf("primary proxy_state missing: %+v err=%v", state, serr)
	}
	if state.Token != mp.Token || state.Endpoint != result.ProxyEndpoint || state.InstanceID == "" {
		t.Errorf("primary state %+v disagrees with managedProxy token/port", state)
	}

	// Authenticated request reaches the member upstream; wrong token → 401.
	req, _ := http.NewRequest(http.MethodPost, result.ProxyEndpoint+"/v1/chat/completions",
		strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+mp.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("proxied request status = %d body=%s", resp.StatusCode, body)
	}

	badReq, _ := http.NewRequest(http.MethodPost, result.ProxyEndpoint+"/v1/chat/completions",
		strings.NewReader(`{}`))
	badReq.Header.Set("Authorization", "Bearer wrong")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want 401", badResp.StatusCode)
	}

	// Undo semantics: StopPoolProxy tears the pool down deterministically.
	if err := env.Px.StopPoolProxy(ctx, "vendorx"); err != nil {
		t.Fatalf("stop pool: %v", err)
	}
	waitPoolDown(t, result.ProxyEndpoint)
	if remaining, _ := env.Px.Jobs.ListPoolLeases("vendorx"); len(remaining) != 0 {
		t.Errorf("pool leases survived stop: %+v", remaining)
	}
}

// Rollback through the pool path: an ApplyProxyEndpoint failure after the pool
// started must stop the WHOLE pool via undo, not leave it bound.
func TestExecutePoolRollbackStopsPool(t *testing.T) {
	env := newPoolTestEnv(t, "")
	ctx := context.Background()

	// Swap in a claude adapter whose Apply always fails.
	reg := client.NewRegistry()
	if err := client.RegisterBuiltin(reg, env.Home); err != nil {
		t.Fatal(err)
	}
	inner, err := reg.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&failingApplyClient{inner: inner}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.AccountPoolRef("vendorx")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	savedClients := env.Deps.Clients
	savedRtClients := env.Rt.Clients
	env.Deps.Clients = reg
	env.Rt.Clients = reg
	defer func() { env.Deps.Clients = savedClients; env.Rt.Clients = savedRtClients }()

	_, execErr := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if execErr == nil || !strings.Contains(execErr.Error(), "injected") {
		t.Fatalf("expected injected failure, got %v", execErr)
	}

	endpoint := plan.Transport.Endpoint
	if endpoint == "" {
		t.Fatal("endpoint was not filled before the fault injected")
	}
	waitPoolDown(t, endpoint)
	if remaining, _ := env.Px.Jobs.ListPoolLeases("vendorx"); len(remaining) != 0 {
		t.Errorf("pool leases survived rollback: %+v", remaining)
	}
	if state, _ := env.Px.Store.ReadProxyState(context.Background(), "vendorx", "a"); state != nil {
		t.Errorf("primary proxy_state survived rollback: %+v", state)
	}
}

// The account branch of startManagedProxy is unchanged: single-account sources
// still route through StartProxy/StopIfLeaseOwned, and unsupported kinds still
// fail closed with TRANSPORT_UNAVAILABLE instead of starting anything.
func TestStartManagedProxyBranchesUnchanged(t *testing.T) {
	env := newPoolTestEnv(t, "")
	ctx := context.Background()

	if _, err := env.Svc.SaveInput(ctx, "vendorx", "single",
		accounts.CredentialInput{Kind: "api-key", Secret: env.UpstreamAuth + "-single", Profile: "global"},
		true); err != nil {
		t.Fatal(err)
	}

	started, err := env.Px.StartProxy(ctx, "vendorx", "single")
	if err != nil {
		t.Fatalf("account StartProxy: %v", err)
	}
	defer func() {
		_ = env.Px.StopIfLeaseOwned(ctx, "vendorx", "single", started.InstanceID, started.LeaseID)
	}()
	if started.Endpoint == "" || started.Token == "" {
		t.Fatalf("account start incomplete: %+v", started)
	}

	gatewayPlan := &Plan{
		ResolvedSource: &ResolvedSource{Ref: refs.Ref{Kind: refs.KindGateway, Name: "gw"}},
	}
	err = startManagedProxy(ctx, gatewayPlan, env.Deps, &activationContext{})
	var ae *anyerr.Error
	if !errorsAs(err, &ae) || ae.Code != "TRANSPORT_UNAVAILABLE" {
		t.Fatalf("unsupported kind error = %v, want TRANSPORT_UNAVAILABLE", err)
	}
}
