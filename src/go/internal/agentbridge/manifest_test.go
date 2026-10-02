package agentbridge

import (
	"context"
	"encoding/json"
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
	a, err := app.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

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
