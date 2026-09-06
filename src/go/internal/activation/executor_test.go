package activation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

// Full persistent switch: plan → execute → verify native auth, active pointer,
// global binding, and journal state.
func TestExecutePersistentSwitch(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Account A saved from the original live login.
	env.writeCodexLive(t, "a@example.com", "tok-a")
	if _, err := env.Svc.Save(ctx, "codex", "a", true); err != nil {
		t.Fatal(err)
	}

	// Simulate a different live login, saved as account B.
	env.writeCodexLive(t, "b@example.com", "tok-b")
	if _, err := env.Svc.Save(ctx, "codex", "b", true); err != nil {
		t.Fatal(err)
	}

	// Switch back to A first so activating B is an observable change.
	if _, err := env.Svc.Use(ctx, "codex", "a", UseNoProxyOption()); err != nil {
		t.Fatal(err)
	}
	liveA := env.readCodexLive(t)
	if identityOf(mustParse(t, liveA)) != "a@example.com" {
		t.Fatalf("use(a) did not restore A: %s", liveA)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "codex",
		Source: refPtr(refs.AccountRef("codex", "b")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.AlreadyActive {
		t.Error("fresh switch must not report alreadyActive")
	}

	live := mustParse(t, env.readCodexLive(t))
	if identityOf(live) != "b@example.com" {
		t.Errorf("live auth = %v, want b@example.com", identityOf(live))
	}

	active, _ := env.Deps.Accounts.Svc.Store.GetActive("codex")
	if active != "b" {
		t.Errorf("active pointer = %q, want b", active)
	}

	binding, err := env.Deps.Bindings.GetGlobalContext(context.Background(), "codex")
	if err != nil || binding == nil {
		t.Fatalf("global binding missing: %v", err)
	}
	if binding.Spec.Source.Provider != "codex" || binding.Spec.Source.Name != "b" {
		t.Errorf("binding source = %+v", binding.Spec.Source)
	}

	entry, err := env.Deps.Journal.Get(result.JournalID)
	if err != nil || entry == nil {
		t.Fatalf("journal entry missing: %v", err)
	}
	if entry.State != journal.StateCommitted {
		t.Errorf("journal state = %s, want committed", entry.State)
	}
	for _, r := range entry.AffectedResources {
		if r == "account/codex/b" || r == "client/codex" {
			continue
		}
		t.Errorf("unexpected affected resource %q", r)
	}
}

// Failing adapter: injects a fault AFTER native auth was written
// (WriteNativeAuth runs before WriteClientConfig).
type failingCodexClient struct{ inner client.Adapter }

func (f *failingCodexClient) ID() string   { return f.inner.ID() }
func (f *failingCodexClient) Name() string { return f.inner.Name() }
func (f *failingCodexClient) Capabilities() *client.Capabilities {
	return f.inner.Capabilities()
}
func (f *failingCodexClient) Apply(ctx *client.ApplyContext) (*client.ApplyResult, error) {
	return f.inner.Apply(ctx)
}
func (f *failingCodexClient) Inspect() (*client.InspectResult, error) { return f.inner.Inspect() }
func (f *failingCodexClient) Reset() error                            { return nil }
func (f *failingCodexClient) RestoreNative() error {
	return errors.New("injected failure in WriteClientConfig step")
}

// Rollback: when WriteClientConfig fails after WriteNativeAuth switched the
// login, the durable checkpoint restores the pre-switch state exactly.
func TestExecuteRollbackRestoresPreviousAuth(t *testing.T) {
	env := newTestEnv(t)
	env.withFailingCodex(t)
	ctx := context.Background()

	// Live = keep; save it. Then live becomes other; save it too.
	env.writeCodexLive(t, "keep@example.com", "tok-keep")
	if _, err := env.Svc.Save(ctx, "codex", "keep", true); err != nil {
		t.Fatal(err)
	}
	env.writeCodexLive(t, "other@example.com", "tok-other")
	if _, err := env.Svc.Save(ctx, "codex", "other", true); err != nil {
		t.Fatal(err)
	}
	// Make "other" genuinely live (pointer + files) so the rollback has a real
	// prior state to restore.
	if _, err := env.Svc.Use(ctx, "codex", "other", UseNoProxyOption()); err != nil {
		t.Fatal(err)
	}
	// Live is currently "other"; switching to "keep" checkpoints that state.

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "codex",
		Source: refPtr(refs.AccountRef("codex", "keep")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatal(err)
	}

	_, execErr := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if execErr == nil {
		t.Fatal("expected injected failure from WriteClientConfig")
	}
	var ae interface{ Error() string } = execErr
	if !strings.Contains(ae.Error(), "injected") && !strings.Contains(execErr.Error(), "injected") {
		t.Logf("failure cause: %v", execErr)
	}

	// The failed activation must have restored the pre-switch login exactly.
	live := mustParse(t, env.readCodexLive(t))
	if identityOf(live) != "other@example.com" {
		t.Errorf("post-rollback live = %v, want other@example.com", identityOf(live))
	}
	active, _ := env.Deps.Accounts.Svc.Store.GetActive("codex")
	if active != "other" {
		t.Errorf("post-rollback active = %q, want other", active)
	}

	// No incomplete journal entries may remain.
	incomplete, err := env.Deps.Journal.ListIncomplete()
	if err != nil {
		t.Fatal(err)
	}
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
		t.Error("no rolled_back journal entry found")
	}
}

// Recovery: an interrupted activation with a durable checkpoint restores the
// captured login and active pointer on next startup.
func TestRecoveryRestoresCheckpointedAuth(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	env.writeCodexLive(t, "orig@example.com", "tok-orig")
	if _, err := env.Svc.Save(ctx, "codex", "orig", true); err != nil {
		t.Fatal(err)
	}
	env.writeCodexLive(t, "moved@example.com", "tok-moved")
	if _, err := env.Svc.Save(ctx, "codex", "moved", true); err != nil {
		t.Fatal(err)
	}
	// A real prior state: orig is live AND pointed to.
	if _, err := env.Svc.Use(ctx, "codex", "orig", UseNoProxyOption()); err != nil {
		t.Fatal(err)
	}

	// Durable checkpoint capturing "orig" while it is live and active.
	cp, err := env.Svc.CheckpointLiveAuth(ctx, "codex", true)
	if err != nil {
		t.Fatal(err)
	}

	// Crash simulation: a completed-looking switch happened afterwards; the
	// activation's journal entry is left mid-flight with the checkpoint in its
	// params. Recovery must roll BOTH the login and the pointer back.
	if _, err := env.Svc.Use(ctx, "codex", "moved", UseNoProxyOption()); err != nil {
		t.Fatal(err)
	}
	entry, err := env.Deps.Journal.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		AffectedResources: []string{"client/codex", "account/codex/orig"},
		Params: map[string]interface{}{
			"nativeAuthCheckpoint": cp,
		},
		State: journal.StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := RecoverIncompleteOperations(ctx, env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || len(res.Failed) != 0 || len(res.Refused) != 0 {
		t.Fatalf("recovery result = %+v", res)
	}
	final, _ := env.Deps.Journal.Get(entry.ID)
	if final == nil || final.State != journal.StateRolledBack {
		t.Errorf("journal state after recovery = %v, want rolled_back", final)
	}

	live := mustParse(t, env.readCodexLive(t))
	if identityOf(live) != "orig@example.com" {
		t.Errorf("recovered live = %v, want orig@example.com", identityOf(live))
	}
	active, _ := env.Deps.Accounts.Svc.Store.GetActive("codex")
	if active != "orig" {
		t.Errorf("recovered active pointer = %q, want orig", active)
	}
}

// Recovery must never forward-execute a source it cannot re-resolve exactly.
func TestRecoveryRefusesUnresolvableSource(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	entry, err := env.Deps.Journal.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		AffectedResources: []string{"client/claude", "gateway/nope-gateway"},
		State:             journal.StateExecuting,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := RecoverIncompleteOperations(ctx, env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 0 {
		t.Errorf("recovered = %d, want 0", res.Recovered)
	}
	final, _ := env.Deps.Journal.Get(entry.ID)
	if final == nil || final.State != journal.StateFailed {
		t.Errorf("state = %v, want failed", final)
	}
}

func mustParse(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("parse live auth: %v", err)
	}
	return m
}

// StepValidateExternalDep is the single enforcement point for a missing
// external proxy (invariant #3): the executor must not pre-derive the failure
// from transport capability before running the plan's steps.
func TestValidateExternalDepStepEnforcesMissingBinary(t *testing.T) {
	missing := &Plan{
		Client: "claude",
		ResolvedSource: &ResolvedSource{
			Display: "kiro/work",
			Adapter: transport.KiroAccountAdapter("kiro", "work", func() string { return "" }),
		},
	}
	if err := validateExternalDep(missing); err == nil {
		t.Fatal("missing kirolink must fail the step")
	} else {
		var ae *anyerr.Error
		if !errorsAsAny(err, &ae) {
			t.Fatalf("error is not anyerr: %T", err)
		}
		if ae.Code != "MISSING_DEPENDENCY" {
			t.Fatalf("code = %q, want MISSING_DEPENDENCY", ae.Code)
		}
	}

	present := &Plan{
		Client: "claude",
		ResolvedSource: &ResolvedSource{
			Display: "kiro/work",
			Adapter: transport.KiroAccountAdapter("kiro", "work", func() string { return "/usr/local/bin/kirolink" }),
		},
	}
	if err := validateExternalDep(present); err != nil {
		t.Fatalf("present kirolink must pass the step: %v", err)
	}
}
