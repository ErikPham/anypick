package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
)

// The accounts-pane switch is one transaction: when the target cannot be
// applied to bound clients (here the reapply cannot pick a default model), the
// previous account is restored instead of leaving the pane on the new account
// while the bound client still points at the old one.
func TestSwitchRollsBackWhenReapplyFails(t *testing.T) {
	env := newTrayEnv(t)
	wireActivation(t, env)
	env.Svc.SetAfterSwitch(env.Deps.Bindings.RealignNativeClientBinding)
	ctx := context.Background()

	authPath := filepath.Join(t.TempDir(), "opencode-auth.json")
	writeLive := func(key string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath,
			[]byte(`{"opencode-go":{"type":"api","key":"`+key+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENCODE_AUTH_PATH", authPath)
	writeLive("sk-a")
	if _, err := env.Svc.Save(ctx, "opencode", "a", false); err != nil {
		t.Fatal(err)
	}
	writeLive("sk-b")
	if _, err := env.Svc.Save(ctx, "opencode", "b", false); err != nil {
		t.Fatal(err)
	}
	if err := env.Svc.SetActive(ctx, "opencode", "a"); err != nil {
		t.Fatal(err)
	}
	// The live file was left holding b's credential by the save above; the
	// switch rollback must restore whatever was live before it, so make that
	// a's.
	writeLive("sk-a")
	// The bound client needs a default model and no catalog is cached, so the
	// reapply of b fails at plan time — a deterministic stand-in for any
	// client-config failure.
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "opencode",
		Spec: bindings.Spec{
			Client: "opencode", Source: refs.AccountRef("opencode", "a"),
			Model: bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	var out syncBuffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	switchID := ""
	for _, row := range snap.Accounts {
		if row.ProviderID == "opencode" && row.Name == "b" {
			switchID = row.SwitchActionID
		}
	}
	if switchID == "" {
		t.Fatalf("no switch action for opencode/b: %+v", snap.Accounts)
	}

	res := invokeAndAwait(t, srv, &out, snap.Revision, switchID, "switch-b")
	if res.Status != "error" {
		t.Fatalf("switch must fail when the reapply cannot be planned: %+v", res)
	}
	if !strings.Contains(res.Message, "previous account and its clients were restored") {
		t.Fatalf("error does not explain the rollback: %q", res.Message)
	}

	if active, _ := env.Deps.Store.GetActive(ctx, "opencode"); active != "a" {
		t.Fatalf("active = %q, want a after rollback", active)
	}
	binding, err := env.Deps.Bindings.GetGlobalContext(ctx, "opencode")
	if err != nil || binding == nil {
		t.Fatalf("binding = %+v, %v", binding, err)
	}
	if got := refs.SerializeRef(binding.Spec.Source); got != "account/opencode/a" {
		t.Fatalf("binding source = %q, want account/opencode/a", got)
	}
	live, err := os.ReadFile(authPath)
	if err != nil || !strings.Contains(string(live), "sk-a") {
		t.Fatalf("live auth was not restored: %s %v", live, err)
	}
}

func TestSwitchGeminiSucceedsWhenBoundClientNeedsDefaultModel(t *testing.T) {
	env := newTrayEnv(t)
	wireActivation(t, env)
	env.Svc.SetAfterSwitch(env.Deps.Bindings.RealignNativeClientBinding)
	ctx := context.Background()

	save := func(name, key string) {
		t.Helper()
		if _, err := env.Svc.SaveInput(ctx, "gemini", name, accounts.CredentialInput{
			Kind: "api-key", Secret: key,
		}, false); err != nil {
			t.Fatalf("save %s failed: %v", name, err)
		}
	}
	save("g1", "key-1")
	save("g2", "key-2")

	if err := env.Svc.SetActive(ctx, "gemini", "g1"); err != nil {
		t.Fatal(err)
	}

	// Bind omp to gemini/g2 with OmittedModel. omp declares RequiresDefaultModel: true.
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "omp",
		Spec: bindings.Spec{
			Client: "omp", Source: refs.AccountRef("gemini", "g2"),
			Model: bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	var out syncBuffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	switchID := ""
	for _, row := range snap.Accounts {
		if row.ProviderID == "gemini" && row.Name == "g2" {
			switchID = row.SwitchActionID
		}
	}
	if switchID == "" {
		t.Fatalf("no switch action for gemini/g2: %+v", snap.Accounts)
	}

	res := invokeAndAwait(t, srv, &out, snap.Revision, switchID, "switch-g2")
	if res.Status != "success" {
		t.Fatalf("switch must succeed, got: %+v", res)
	}

	if active, _ := env.Deps.Store.GetActive(ctx, "gemini"); active != "g2" {
		t.Fatalf("active = %q, want g2", active)
	}
	binding, err := env.Deps.Bindings.GetGlobalContext(ctx, "omp")
	if err != nil || binding == nil {
		t.Fatalf("binding = %+v, %v", binding, err)
	}
	if got := refs.SerializeRef(binding.Spec.Source); got != "account/gemini/g2" {
		t.Fatalf("binding source = %q, want account/gemini/g2", got)
	}
	// Verify omp harness config file was written with the fallback model
	ompConfig := filepath.Join(env.Home, ".omp", "agent", "config.yml")
	content, err := os.ReadFile(ompConfig)
	if err != nil {
		t.Fatalf("reading omp config: %v", err)
	}
	if !strings.Contains(string(content), "defaultModel: 'anypick/gemini-3.1-pro-preview'") {
		t.Fatalf("omp config does not contain fallback model: %s", string(content))
	}
}
