package activation

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// newClientRegistryWithFailingApply builds a registry whose codex adapter
// fails Apply, for fault injection in the proxy branch.
func newClientRegistryWithFailingApply(t *testing.T, env *testEnv) *client.Registry {
	t.Helper()
	reg := client.NewRegistry()
	if err := client.RegisterBuiltin(reg, env.Home); err != nil {
		t.Fatal(err)
	}
	inner, err := reg.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&failingApplyClient{inner: inner}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// Full managed-proxy activation: policy=proxy starts a real loopback proxy,
// injects its endpoint into the client config, and forwards authenticated
// requests to the upstream with provider credentials.
func TestExecuteManagedProxyActivation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	var upstreamAuths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		upstreamAuths = append(upstreamAuths, auth)
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upstream":true}`))
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	env.UpstreamURL = u

	// Account with an enabled proxy config (port 0 → kernel-assigned).
	env.writeCodexLive(t, "px@example.com", "tok-px")
	if _, err := env.Svc.Save(ctx, "codex", "px", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(context.Background(), "codex", "px",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:            ModePersistent,
		Client:          "codex",
		Source:          refPtr(refs.AccountRef("codex", "px")),
		TransportPolicy: "proxy",
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
	defer func() { _ = env.Px.Stop(ctx, "codex", "px") }()

	// Client config must now carry the proxy endpoint and managed block.
	configRaw := readCodexConfig(t, env)
	if !strings.Contains(configRaw, result.ProxyEndpoint) {
		t.Errorf("config.toml missing proxy endpoint %s:\n%s", result.ProxyEndpoint, configRaw)
	}
	if !strings.Contains(configRaw, "BEGIN anypick-managed") {
		t.Errorf("config.toml missing managed marker:\n%s", configRaw)
	}

	// A request through the proxy reaches upstream with injected credentials;
	// the proxy token itself must never be forwarded. The client presents the
	// proxy token as its API key (env_key ANYPICK_PROXY_API_KEY).
	token := plan.Transport.ManagedProxy.Token
	if token == "" {
		t.Fatal("activation did not carry a proxy token")
	}
	req, _ := http.NewRequest("POST", result.ProxyEndpoint+"/v1/chat/completions",
		strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("proxied request status = %d body=%s", resp.StatusCode, body)
	}
	if len(upstreamAuths) != 1 || upstreamAuths[0] != "Bearer "+env.UpstreamAuth {
		t.Errorf("upstream saw auth %v, want Bearer %s", upstreamAuths, env.UpstreamAuth)
	}

	// Wrong credentials are rejected by the proxy itself.
	badReq, _ := http.NewRequest("POST", result.ProxyEndpoint+"/v1/chat/completions",
		strings.NewReader(`{}`))
	badReq.Header.Set("Authorization", "Bearer wrong-token")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want 401", badResp.StatusCode)
	}

	// Health endpoint is unauthenticated but echoes ownership.
	healthResp, err := http.Get(result.ProxyEndpoint + "/health")
	if err != nil {
		t.Fatal(err)
	}
	healthBody, _ := io.ReadAll(healthResp.Body)
	_ = healthResp.Body.Close()
	if !strings.Contains(string(healthBody), `"instanceId"`) {
		t.Errorf("health body missing instanceId: %s", healthBody)
	}

	// Journal committed; lease recorded and owned by this process.
	entry, _ := env.Deps.Journal.Get(result.JournalID)
	if entry == nil || entry.State != journal.StateCommitted {
		t.Fatalf("journal = %+v", entry)
	}
	leases, err := env.Px.Jobs.ListByAccountContext(ctx, "codex", "px")
	if err != nil || len(leases) == 0 {
		t.Fatalf("leases = %+v err=%v", leases, err)
	}
}

// failingApplyClient fails Apply — the proxy branch of WriteClientConfig.
type failingApplyClient struct{ inner client.Adapter }

func (f *failingApplyClient) ID() string   { return f.inner.ID() }
func (f *failingApplyClient) Name() string { return f.inner.Name() }
func (f *failingApplyClient) Capabilities() *client.Capabilities {
	return f.inner.Capabilities()
}
func (f *failingApplyClient) Apply(_ *client.ApplyContext) (*client.ApplyResult, error) {
	return nil, errors.New("injected failure in ApplyProxyEndpoint")
}
func (f *failingApplyClient) Inspect() (*client.InspectResult, error) {
	return f.inner.Inspect()
}
func (f *failingApplyClient) Reset() error { return nil }

// Rollback through the proxy path: an ApplyProxyEndpoint failure must stop the
// started proxy, release its lease, and restore client config compensation.
func TestExecuteProxyRollbackStopsProxy(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	env.UpstreamURL = u

	env.writeCodexLive(t, "rb@example.com", "tok-rb")
	if _, err := env.Svc.Save(ctx, "codex", "rb", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(context.Background(), "codex", "rb",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// Swap in a codex adapter whose Apply always fails.
	reg := newClientRegistryWithFailingApply(t, env)

	plan, err := PlanActivation(ctx, Request{
		Mode:            ModePersistent,
		Client:          "codex",
		Source:          refPtr(refs.AccountRef("codex", "rb")),
		TransportPolicy: "proxy",
	}, env.Deps, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Point Deps AND the runtime service at the failing adapter AFTER planning
	// so resolution uses the real one but execution hits the fault.
	savedClients := env.Deps.Clients
	env.Deps.Clients = reg
	savedRtClients := env.Rt.Clients
	env.Rt.Clients = reg
	defer func() { env.Deps.Clients = savedClients; env.Rt.Clients = savedRtClients }()

	_, execErr := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if execErr == nil || !strings.Contains(execErr.Error(), "injected") {
		t.Fatalf("expected injected failure, got %v", execErr)
	}

	// Proxy stopped, lease gone, state cleared.
	status, err := env.Px.Status("codex", "rb")
	if err != nil {
		t.Fatal(err)
	}
	if status.Running {
		t.Error("proxy still running after rollback")
	}
	leases, _ := env.Px.Jobs.ListByAccountContext(ctx, "codex", "rb")
	if len(leases) != 0 {
		t.Errorf("leases survived rollback: %+v", leases)
	}

	// No config.toml should have been left behind (delete=> compensation).
	if _, statErr := os.Stat(filepath.Join(env.Home, ".codex", "config.toml")); statErr == nil {
		t.Error("config.toml survived rollback though it did not exist pre-activation")
	}

	incomplete, _ := env.Deps.Journal.ListIncomplete()
	for _, e := range incomplete {
		t.Errorf("journal still incomplete: %s (%s)", e.ID, e.State)
	}
	recent, _ := env.Deps.Journal.ListRecent(5)
	foundRolledBack := false
	for _, e := range recent {
		if e.State == journal.StateRolledBack {
			foundRolledBack = true
		}
	}
	if !foundRolledBack {
		t.Error("no rolled_back journal entry")
	}
}

func readCodexConfig(t *testing.T, env *testEnv) string {
	t.Helper()
	path := filepath.Join(env.Home, ".codex", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	return string(data)
}
