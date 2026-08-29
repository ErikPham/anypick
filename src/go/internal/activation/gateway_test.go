package activation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/refs"
)

func seedGateway(t *testing.T, env *testEnv, name string) {
	t.Helper()
	if _, err := env.Deps.Profiles.Upsert(profile.Runtime{
		Meta: profile.Meta{
			Name:         name,
			Provider:     "openrouter",
			Endpoint:     "https://openrouter.example/api/v1",
			DefaultModel: "vendor/large",
			Models:       map[string]string{"default": "vendor/large"},
		},
		Secrets: profile.Secrets{APIKey: "sk-or-test"},
	}); err != nil {
		t.Fatal(err)
	}
	env.Deps.Catalog.Set("openrouter", CatalogEntry{
		Name:     "OpenRouter",
		APIStyle: "custom", // dual-protocol
	})
}

// Gateway activation: claude × gateway → direct with profile apply.
func TestExecuteGatewayActivation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedGateway(t, env, "or-work")

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.GatewayRef("or-work")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Transport.Capability != "direct" {
		t.Fatalf("capability = %s, want direct (dual-protocol gateway)", plan.Transport.Capability)
	}
	for _, s := range plan.Steps {
		if s.Kind == StepWriteNativeAuth {
			t.Fatal("gateway plans must never include WriteNativeAuth")
		}
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	settingsRaw, err := os.ReadFile(filepath.Join(env.Home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(settingsRaw, &doc); err != nil {
		t.Fatal(err)
	}
	// Smart routing strips /v1 for Anthropic-protocol clients (Claude Code
	// appends /v1/messages itself).
	if doc.Env["ANTHROPIC_BASE_URL"] != "https://openrouter.example/api" {
		t.Errorf("BASE_URL = %q, want https://openrouter.example/api", doc.Env["ANTHROPIC_BASE_URL"])
	}
	// Non-official endpoint must use AUTH_TOKEN, never API_KEY.
	if _, ok := doc.Env["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("official-only API key injected for third-party endpoint: %+v", doc.Env)
	}
	if doc.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-or-test" {
		t.Errorf("AUTH_TOKEN = %q, want sk-or-test", doc.Env["ANTHROPIC_AUTH_TOKEN"])
	}
	// Model roles flow through with provider prefixes stripped.
	if doc.Env["ANTHROPIC_MODEL"] != "large" {
		t.Errorf("ANTHROPIC_MODEL = %q, want large", doc.Env["ANTHROPIC_MODEL"])
	}

	binding, _ := env.Deps.Bindings.GetGlobal("claude")
	if binding == nil || binding.Spec.Source.Kind != refs.KindGateway ||
		binding.Spec.Source.Name != "or-work" {
		t.Fatalf("binding = %+v", binding)
	}

	entry, _ := env.Deps.Journal.Get(result.JournalID)
	if entry == nil || entry.State != journal.StateCommitted {
		t.Fatalf("journal = %+v", entry)
	}

	// VerifyEffectiveState reads back the config without warnings.
	foundWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "reports no anypick-managed configuration") {
			foundWarning = true
		}
	}
	if foundWarning {
		t.Errorf("unexpected verify warning: %+v", result.Warnings)
	}
}

func TestPlannerRejectsUnknownGateway(t *testing.T) {
	env := newTestEnv(t)
	_, err := PlanActivation(context.Background(), Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.GatewayRef("missing")),
	}, env.Deps, Options{})
	var ae *anyerr.Error
	if !errorsAsAny(err, &ae) || ae.Code != "GATEWAY_NOT_FOUND" {
		t.Fatalf("err = %v, want GATEWAY_NOT_FOUND", err)
	}
}

// A single-protocol catalog entry narrows pairings: openai-only gateways are
// unsupported for claude even though the client exists.
func TestGatewayProtocolNarrowing(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedGateway(t, env, "or-openai")
	env.Deps.Catalog.Set("openrouter", CatalogEntry{Name: "OR", APIStyle: "openai"})

	_, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.GatewayRef("or-openai")),
	}, env.Deps, Options{})
	var ae *anyerr.Error
	if !errorsAsAny(err, &ae) || ae.Code != "UNSUPPORTED_TRANSPORT" {
		t.Fatalf("err = %v, want UNSUPPORTED_TRANSPORT", err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "codex",
		Source: refPtr(refs.GatewayRef("or-openai")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("codex × openai gateway should be direct: %v", err)
	}
	if plan.Transport.Capability != "direct" {
		t.Fatalf("capability = %s", plan.Transport.Capability)
	}
}
