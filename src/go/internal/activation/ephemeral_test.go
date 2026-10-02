package activation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// Ephemeral run through the proxy path: isolated home is created with the
// proxy endpoint baked in; cleanup reverses everything (proxy, lease, temp
// dir) exactly once.
func TestExecuteEphemeralRunWithIsolatedHome(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	env.UpstreamURL = u

	env.writeCodexLive(t, "run@example.com", "tok-run")
	if _, err := env.Svc.Save(ctx, "codex", "run", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(context.Background(), "codex", "run",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:            ModeEphemeral,
		Client:          "codex",
		Source:          refPtr(refs.AccountRef("codex", "run")),
		TransportPolicy: "proxy",
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Cleanup == nil {
		t.Fatal("ephemeral result must carry a cleanup closure")
	}
	if result.Isolated == nil || result.Isolated.Directory == "" {
		t.Fatal("isolated runtime missing")
	}

	// Isolated config.toml carries the proxy endpoint; live config untouched.
	isoConfig := filepath.Join(result.Isolated.Environment["CODEX_HOME"], "config.toml")
	raw, err := os.ReadFile(isoConfig)
	if err != nil {
		t.Fatalf("isolated config: %v", err)
	}
	if !strings.Contains(string(raw), result.ProxyEndpoint) {
		t.Errorf("isolated config missing endpoint:\n%s", raw)
	}
	liveCfg := filepath.Join(env.Home, ".codex", "config.toml")
	if _, err := os.Stat(liveCfg); !os.IsNotExist(err) {
		t.Error("live config.toml must not exist after an ephemeral run")
	}

	// Proxy alive during the session.
	status, _ := env.Px.Status("codex", "run")
	if !status.Running {
		t.Error("proxy must be running during the session")
	}

	// Journal stays recoverable until cleanup ran (SIGKILL safety).
	entry, _ := env.Deps.Journal.Get(result.JournalID)
	if entry.State == journal.StateCommitted {
		t.Error("ephemeral journal must stay recoverable before cleanup")
	}

	cleanupErrs := make(chan error, 2)
	startCleanup := make(chan struct{})
	for range 2 {
		go func() {
			<-startCleanup
			cleanupErrs <- result.Cleanup(ctx)
		}()
	}
	close(startCleanup)
	for range 2 {
		if err := <-cleanupErrs; err != nil {
			t.Fatalf("concurrent cleanup: %v", err)
		}
	}
	// Idempotent.
	if err := result.Cleanup(ctx); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}

	if _, err := os.Stat(result.Isolated.Directory); !os.IsNotExist(err) {
		t.Error("isolated directory survived cleanup")
	}
	status, _ = env.Px.Status("codex", "run")
	if status.Running {
		t.Error("proxy still running after cleanup")
	}
	leases, _ := env.Px.Jobs.ListByAccountContext(ctx, "codex", "run")
	if len(leases) != 0 {
		t.Errorf("leases survived cleanup: %+v", leases)
	}
	final, _ := env.Deps.Journal.Get(result.JournalID)
	if final.State != journal.StateRolledBack && final.State != journal.StateCommitted &&
		final.State != journal.StateFailed {
		t.Errorf("unexpected terminal state %s", final.State)
	}
}

// The ephemeral gate already rejects native-auth direct runs at planning;
// this asserts the same gate exists for gemini-style adapters via policy.
func TestEphemeralProxyRunKeepsNativeAuthUntouched(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	env.UpstreamURL = u

	env.writeCodexLive(t, "live@example.com", "tok-live")
	if _, err := env.Svc.Save(ctx, "codex", "acct", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(context.Background(), "codex", "acct",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := env.readCodexLive(t)

	plan, err := PlanActivation(ctx, Request{
		Mode:            ModeEphemeral,
		Client:          "codex",
		Source:          refPtr(refs.AccountRef("codex", "acct")),
		TransportPolicy: "proxy",
	}, env.Deps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.Kind == StepWriteNativeAuth {
			t.Fatal("ephemeral plans must never include WriteNativeAuth")
		}
	}
	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Cleanup(ctx); _ = env.Px.Stop(ctx, "codex", "acct") }()

	if got := env.readCodexLive(t); got != before {
		t.Error("ephemeral run mutated live native auth")
	}
	active, _ := env.Deps.Accounts.Svc.Store.GetActive(context.Background(), "codex")
	if active == "acct" {
		t.Error("ephemeral run must not move the active pointer")
	}
}
