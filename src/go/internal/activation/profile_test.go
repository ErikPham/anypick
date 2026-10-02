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

	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transport"
)

func TestCodexProfileActivationKeepsRoutesAndAuthIndependent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	env.UpstreamURL, _ = url.Parse(upstream.URL)

	env.writeCodexLive(t, "profile@example.com", "live-token")
	if _, err := env.Svc.Save(ctx, "codex", "profile", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(ctx, "codex", "profile",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	beforeAuth := env.readCodexLive(t)

	activate := func(name, model string) {
		t.Helper()
		plan, err := PlanActivation(ctx, Request{
			Mode: ModeProfile, Client: "codex", ProfileName: name,
			Source: refPtr(refs.AccountRef("codex", "profile")), Model: model,
			TransportPolicy: transport.PolicyProxy,
		}, env.Deps, Options{})
		if err != nil {
			t.Fatalf("plan %s: %v", name, err)
		}
		if plan.Transport.Capability != "managed_builtin_proxy" {
			t.Fatalf("profile %s capability = %s", name, plan.Transport.Capability)
		}
		for _, step := range plan.Steps {
			if step.Kind == StepWriteNativeAuth {
				t.Fatalf("profile %s must not write shared auth.json", name)
			}
		}
		result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
		if err != nil {
			t.Fatalf("execute %s: %v", name, err)
		}
		entry, err := env.Deps.Journal.Get(result.JournalID)
		if err != nil || entry.State != journal.StateCommitted {
			t.Fatalf("profile %s journal = %#v, %v", name, entry, err)
		}
	}
	activate("a", "model-a")
	activate("b", "model-b")

	for name, model := range map[string]string{"a": "model-a", "b": "model-b"} {
		configPath := client.CodexProfileConfigPathForRoot(env.Root, name)
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read profile %s: %v", name, err)
		}
		text := string(raw)
		if !strings.Contains(text, "model = \""+model+"\"") {
			t.Fatalf("profile %s lost its model:\n%s", name, text)
		}
		if !strings.Contains(text, "anypick-managed") {
			t.Fatalf("profile %s missing managed route:\n%s", name, text)
		}
		auth, err := os.ReadFile(client.CodexProfileAuthPath(env.Root, name))
		if err != nil || !strings.Contains(string(auth), "live-token") {
			t.Fatalf("profile %s auth snapshot missing: %q %v", name, auth, err)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("profile activation touched global config.toml: %v", err)
	}
	if got := env.readCodexLive(t); got != beforeAuth {
		t.Fatal("profile activation changed the shared Codex auth.json")
	}
	for _, name := range []string{"a", "b"} {
		profile, err := env.ClientProfiles.GetContext(ctx, "codex", name)
		if err != nil || profile == nil {
			t.Fatalf("stored profile %s = %#v, %v", name, profile, err)
		}
		if profile.Spec.Source != (refs.AccountRef("codex", "profile")) {
			t.Fatalf("profile %s source = %#v", name, profile.Spec.Source)
		}
		if profile.Spec.Model.ID != "model-"+name || profile.Spec.TransportPolicy != "proxy" {
			t.Fatalf("profile %s spec = %#v", name, profile.Spec)
		}
	}
	if binding, err := env.Deps.Bindings.GetGlobalContext(ctx, "codex"); err != nil || binding != nil {
		t.Fatalf("named profile should not create global binding: %#v, %v", binding, err)
	}
	if err := env.Px.Stop(ctx, "codex", "profile"); err != nil {
		t.Fatalf("stop test proxy: %v", err)
	}
}

func TestCodexNativeProfileUsesItsOwnAuthHome(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.writeCodexLive(t, "a@example.com", "token-a")
	if _, err := env.Svc.Save(ctx, "codex", "a", true); err != nil {
		t.Fatal(err)
	}
	env.writeCodexLive(t, "b@example.com", "token-b")
	if _, err := env.Svc.Save(ctx, "codex", "b", true); err != nil {
		t.Fatal(err)
	}
	// The desktop app is still signed in as A when the profile is activated.
	env.writeCodexLive(t, "a@example.com", "token-a")
	before := env.readCodexLive(t)

	ref := refs.AccountRef("codex", "b")
	plan, err := PlanActivation(ctx, Request{
		Mode: ModeProfile, Client: "codex", ProfileName: "b", Source: &ref,
	}, env.Deps, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Transport.Capability != transport.Direct {
		t.Fatalf("native profile capability = %s, want direct", plan.Transport.Capability)
	}
	for _, step := range plan.Steps {
		if step.Kind == StepStartProxy || step.Kind == StepWriteNativeAuth {
			t.Fatalf("native profile unexpectedly planned %s", step.Kind)
		}
	}
	if _, err := Execute(ctx, plan, env.Deps, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}

	if got := env.readCodexLive(t); got != before {
		t.Fatal("native profile activation changed the desktop auth.json")
	}
	auth, err := os.ReadFile(client.CodexProfileAuthPath(env.Root, "b"))
	if err != nil || !strings.Contains(string(auth), "token-b") || strings.Contains(string(auth), "token-a") {
		t.Fatalf("profile b auth = %q, %v", auth, err)
	}
	config, err := os.ReadFile(client.CodexProfileConfigPathForRoot(env.Root, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "model_provider = \"openai\"") ||
		strings.Contains(string(config), "base_url") || strings.Contains(string(config), "anypick-managed\n") {
		t.Fatalf("native profile config is not native:\n%s", config)
	}
	inspected, err := env.Deps.Clients.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	profileInspector, ok := inspected.(client.ProfileInspector)
	if !ok {
		t.Fatal("codex profile inspector missing")
	}
	state, err := profileInspector.InspectProfile("b")
	if err != nil || !state.Present {
		t.Fatalf("native profile inspection = %#v, %v", state, err)
	}
}
