package agentbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/app"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/pubgate"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

type testAgentAdapter struct {
	id string
}

func (a testAgentAdapter) ID() string { return a.id }

func (a testAgentAdapter) Name() string { return a.id + " worker" }

func (a testAgentAdapter) Capabilities() *client.Capabilities {
	return &client.Capabilities{
		ID:                         a.id,
		AcceptedProtocols:          []transport.Protocol{transport.Anthropic, transport.OpenAI},
		SupportsEnvironmentOverlay: true,
		SupportsIsolatedHome:       true,
		SupportsPersistentConfig:   true,
	}
}

func (a testAgentAdapter) Apply(*client.ApplyContext) (*client.ApplyResult, error) {
	return &client.ApplyResult{}, nil
}

func (a testAgentAdapter) Reset() error { return nil }

func (a testAgentAdapter) Inspect() (*client.InspectResult, error) {
	return &client.InspectResult{Present: true}, nil
}

func TestBuildManifestUsesSortedSafeClientMetadata(t *testing.T) {
	gate := pubgate.New()
	clients := client.NewRegistry()
	clients.SetGate(gate)
	if err := clients.Register(testAgentAdapter{id: "z-worker"}); err != nil {
		t.Fatal(err)
	}
	if err := clients.Register(testAgentAdapter{id: "a-worker"}); err != nil {
		t.Fatal(err)
	}

	manifest, err := Build(context.Background(), Deps{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != ManifestVersion || len(manifest.Agents) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.Agents[0].ID != "a-worker" || manifest.Agents[1].ID != "z-worker" {
		t.Fatalf("agents not sorted: %+v", manifest.Agents)
	}
	if manifest.Agents[0].Status != "unconfigured" {
		t.Fatalf("status = %q, want unconfigured", manifest.Agents[0].Status)
	}
	if got := manifest.Agents[0].Capabilities.Protocols; len(got) != 2 || got[0] != "anthropic" || got[1] != "openai" {
		t.Fatalf("protocols = %v", got)
	}
}

func TestBuildManifestResolvesBindingThroughActivation(t *testing.T) {
	root := t.TempDir()
	// A temp Home, because app.New with an empty Home binds every provider and
	// client adapter to os.UserHomeDir() and Build then Inspects each client,
	// which reads <home>/.claude/settings.json. That made this test depend on
	// the developer's live machine and read a real credential file.
	// TestBuildManifestReadsOnlyTheInjectedHome is what proves the isolation,
	// because a resolved route hides Inspect's result from this test's
	// assertions.
	home := t.TempDir()
	a, cleanup, err := app.Ready(context.Background(), app.Options{
		Root: root, Home: home, SkipMigrate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	if _, err := a.Runtime.ProfileSvc.UpsertContext(ctx, profile.Runtime{
		Meta: profile.Meta{
			Name: "or-work", Provider: "openrouter",
			Endpoint: "https://gateway.example/v1",
		},
		Secrets: profile.Secrets{APIKey: "must-not-appear"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BindingSvc.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client:          "claude",
			Source:          refs.GatewayRef("or-work"),
			Model:           bindings.ExplicitModel("vendor/model"),
			TransportPolicy: "auto",
		},
	}); err != nil {
		t.Fatal(err)
	}

	manifest, err := Build(ctx, Deps{
		Activation: a.Deps, Clients: a.Clients,
		Bindings: a.BindingSvc, Proxy: a.Proxy,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range manifest.Agents {
		if agent.ID != "claude" {
			continue
		}
		if agent.Route == nil || agent.Route.Source != "or-work" ||
			agent.Route.Model != "vendor/model" || !agent.Route.Ready {
			t.Fatalf("claude manifest entry = %+v", agent)
		}
		if agent.Route.Capability != string(transport.Direct) {
			t.Fatalf("claude capability = %q, want direct", agent.Route.Capability)
		}
		raw, marshalErr := json.Marshal(manifest)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, secret := range []string{"must-not-appear", "https://gateway.example/v1"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("manifest leaked %q: %s", secret, raw)
			}
		}
		return
	}
	t.Fatal("claude agent missing from manifest")
}

// TestBuildManifestReadsOnlyTheInjectedHome is the isolation gate for the test
// above. That test composes through app.Ready with a temp Home so Build's
// adapter.Inspect() cannot reach the developer's real ~/.claude/settings.json,
// but its own assertions cannot show that: a resolved route sets Status to
// "ready" regardless of what Inspect saw, so an ambient home would leave it
// green. This test has no binding, which is what makes Inspect's result visible
// in the status.
//
// The proof is a sentinel in a home the app is NOT given. Build reads the
// injected home, which has no .claude/settings.json, so claude stays
// "unavailable". If a change made app.Ready fall back to the ambient home, the
// planted file would be read and the status would become "unconfigured",
// failing here with the ambient path named.
func TestBuildManifestReadsOnlyTheInjectedHome(t *testing.T) {
	root := t.TempDir()
	injected := t.TempDir()
	// The ambient home is what os.UserHomeDir() resolves, so the sentinel has
	// to live there for the fallback to be observable. Pointing HOME at a temp
	// dir keeps this test off the developer's real machine.
	ambient := t.TempDir()
	t.Setenv("HOME", ambient)
	planted := filepath.Join(ambient, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(planted), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte(`{"anypick":"ambient-sentinel"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Ask the adapter directly rather than reading a status off the manifest.
	// A resolved route overwrites Agent.Status with "ready", so the manifest
	// cannot show which home Inspect read; the adapter's own issue text can.
	// An injected home with no settings.json reports "settings.json not found",
	// while the ambient home that HAS the sentinel reports a different issue
	// ("no anypick-managed env block"), which is the tell.
	a, cleanup, err := app.Ready(context.Background(), app.Options{
		Root: root, Home: injected, SkipMigrate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	cad, cerr := a.Clients.Get("claude")
	if cerr != nil {
		t.Fatal(cerr)
	}
	inspection, ierr := cad.Inspect()
	if ierr != nil {
		t.Fatal(ierr)
	}
	if inspection == nil {
		t.Fatal("claude Inspect returned nothing")
	}
	if inspection.Present {
		t.Fatalf("claude reports a present config in an empty injected home: it read %s", ambient)
	}
	for _, issue := range inspection.Issues {
		if !strings.Contains(issue, "settings.json not found") {
			t.Fatalf("claude Inspect issue = %q, want \"settings.json not found\": the injected home is empty, so this issue came from %s",
				issue, ambient)
		}
	}
}
