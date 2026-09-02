package activation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transport"
)

func refPtr(r refs.Ref) *refs.Ref { return &r }

// Persistent direct activation: codex client × codex account.
func TestPlannerPersistentDirectStepOrder(t *testing.T) {
	env := newTestEnv(t)
	env.writeCodexLive(t, "work@example.com", "tok-work")
	if _, err := env.Svc.Save(context.Background(), "codex", "work", true); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(context.Background(), Request{
		Mode:   ModePersistent,
		Client: "codex",
		Source: refPtr(refs.AccountRef("codex", "work")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	var got []StepKind
	for _, s := range plan.Steps {
		got = append(got, s.Kind)
	}
	want := []StepKind{
		StepResolveSource,
		StepValidateCompatibility,
		StepValidateCredential,
		StepResolveTransport,
		StepWriteNativeAuth,
		StepInspectClientState,
		StepWriteClientConfig,
		StepVerifyEffectiveState,
		StepCommitGlobalBinding,
	}
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %v, want %v", got, want)
		}
	}

	foundRollback := false
	for _, r := range plan.Rollback {
		if r.Kind == "RestoreNativeAuth" {
			foundRollback = true
		}
	}
	if !foundRollback {
		t.Error("plan rollback must contain RestoreNativeAuth")
	}
	if plan.Transport.Capability != "direct" || plan.Transport.Protocol != "openai" {
		t.Errorf("transport = %s/%s, want direct/openai",
			plan.Transport.Capability, plan.Transport.Protocol)
	}
}

// INVARIANT #1 regression at the planner level: codex accounts never serve claude.
func TestPlannerRejectsCrossProviderPairing(t *testing.T) {
	env := newTestEnv(t)
	env.writeCodexLive(t, "x@example.com", "tok")
	if _, err := env.Svc.Save(context.Background(), "codex", "x", true); err != nil {
		t.Fatal(err)
	}

	_, err := PlanActivation(context.Background(), Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.AccountRef("codex", "x")),
	}, env.Deps, Options{})
	if err == nil {
		t.Fatal("expected UNSUPPORTED_TRANSPORT")
	}
	var ae *anyerr.Error
	if !errorsAsAny(err, &ae) || ae.Code != "UNSUPPORTED_TRANSPORT" {
		t.Fatalf("err = %v, want UNSUPPORTED_TRANSPORT", err)
	}
}

// Ephemeral runs must never switch live native auth (planner safety gate).
func TestPlannerEphemeralNativeAuthGate(t *testing.T) {
	env := newTestEnv(t)
	env.writeCodexLive(t, "e@example.com", "tok")
	if _, err := env.Svc.Save(context.Background(), "codex", "e", true); err != nil {
		t.Fatal(err)
	}

	_, err := PlanActivation(context.Background(), Request{
		Mode:   ModeEphemeral,
		Client: "codex",
		Source: refPtr(refs.AccountRef("codex", "e")),
	}, env.Deps, Options{})
	if err == nil {
		t.Fatal("expected ephemeral gate to reject requires-native-auth direct source")
	}
	var ae *anyerr.Error
	if !errorsAsAny(err, &ae) || ae.Code != "UNSUPPORTED_TRANSPORT" {
		t.Fatalf("err = %v, want UNSUPPORTED_TRANSPORT", err)
	}
}

// Missing source is an invalid usage, not a crash.
func TestPlannerRequiresSource(t *testing.T) {
	env := newTestEnv(t)
	_, err := PlanActivation(context.Background(), Request{
		Mode: ModePersistent, Client: "claude",
	}, env.Deps, Options{})
	if err == nil {
		t.Fatal("expected MISSING_SOURCE")
	}
	var ae *anyerr.Error
	if !errorsAsAny(err, &ae) || ae.Code != "MISSING_SOURCE" {
		t.Fatalf("err = %v, want MISSING_SOURCE", err)
	}
}

// Unknown model selection on a migrated binding warns but still plans.
func TestPlannerWarnsOnUnknownModel(t *testing.T) {
	env := newTestEnv(t)
	env.writeCodexLive(t, "u@example.com", "tok")
	if _, err := env.Svc.Save(context.Background(), "codex", "u", true); err != nil {
		t.Fatal(err)
	}
	spec := bindings.Spec{
		Client:          "codex",
		Source:          refs.AccountRef("codex", "u"),
		Model:           bindings.ModelSelection{Mode: "unknown", Reason: "legacy_migration"},
		TransportPolicy: "auto",
	}
	plan, err := PlanActivation(context.Background(), Request{
		Mode: ModePersistent, Client: "codex",
	}, env.Deps, Options{ReapplySpec: &spec, ReapplyProv: &bindings.Provenance{Kind: "direct"}})
	if err != nil {
		t.Fatalf("reapply plan: %v", err)
	}
	found := false
	for _, w := range plan.Warnings {
		if w.Code == "MODEL_UNKNOWN" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected MODEL_UNKNOWN warning, got %+v", plan.Warnings)
	}
	if plan.BindingSpec == nil ||
		!strings.Contains(stringifyRef(plan.BindingSpec.Source), "codex") {
		t.Errorf("binding spec source = %+v", plan.BindingSpec)
	}
}

func stringifyRef(r refs.Ref) string {
	data, _ := json.Marshal(r)
	return string(data)
}

// TS parity (activation-executor.ts WriteNativeAuth): persistent plans for an
// account source that requires a native auth write include WriteNativeAuth
// even when the transport is a managed proxy — `use claude --with grok/x`
// must restore grok's saved login as its live one. The old Go planner gated
// the step behind capability == direct, so cross-client proxy activations
// silently left the source provider's live auth stale.
func TestPlannerManagedProxyAccountStillWritesNativeAuth(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// codex accounts back claude through the managed builtin proxy.
	env.writeOpenCodeLive(t, "x@example.com")
	if _, err := env.Svc.Save(ctx, "opencode", "x", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(context.Background(), "opencode", "x",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:            ModePersistent,
		Client:          "claude",
		Source:          refPtr(refs.AccountRef("opencode", "x")),
		TransportPolicy: "proxy",
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Transport.Capability != transport.ManagedBuiltinProxy {
		t.Fatalf("capability = %s, want managed_builtin_proxy", plan.Transport.Capability)
	}
	found := false
	for _, s := range plan.Steps {
		if s.Kind == StepWriteNativeAuth {
			found = true
		}
	}
	if !found {
		t.Fatal("persistent managed-proxy account plan must include WriteNativeAuth")
	}
	// Rollback must still be able to restore the pre-activation live login.
	foundRestore := false
	for _, r := range plan.Rollback {
		if r.Kind == "RestoreNativeAuth" {
			foundRestore = true
		}
	}
	if !foundRestore {
		t.Fatal("plan rollback must contain RestoreNativeAuth when WriteNativeAuth is planned")
	}
}
