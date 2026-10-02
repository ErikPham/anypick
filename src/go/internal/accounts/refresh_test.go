package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestSaveLiveIntoExistingAccount(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	seedGrokLive(t, "orig@grok.xai")
	if _, err := svc.Save(ctx, "grok", "acc-a", false); err != nil {
		t.Fatal(err)
	}
	seedGrokLive(t, "refreshed@grok.xai")
	pv, err := svc.Providers.Get("grok")
	if err != nil {
		t.Fatal(err)
	}
	name, err := svc.saveLiveIntoExistingAccount(ctx, pv, "acc-a")
	if err != nil {
		t.Fatalf("saveLiveIntoExistingAccount: %v", err)
	}
	if name != "acc-a" {
		t.Fatalf("name=%q want acc-a", name)
	}
	got, _ := st.Get(context.Background(), "grok", "acc-a")
	if got.Meta.Identity != "refreshed@grok.xai" {
		t.Fatalf("Identity=%q want refreshed@grok.xai", got.Meta.Identity)
	}
}

func TestSaveLiveIntoExistingAccountMissing(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	pv, err := svc.Providers.Get("grok")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.saveLiveIntoExistingAccount(ctx, pv, "nope")
	if err == nil {
		t.Fatal("want error for missing account")
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("want ACCOUNT_NOT_FOUND, got %v", err)
	}
}

func TestRestoreCheckpointQuiet(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	seedGrokLive(t, "cp@grok.xai")
	cp, err := svc.CheckpointLiveAuth(ctx, "grok", false)
	if err != nil {
		t.Fatal(err)
	}
	seedGrokLive(t, "mutated@grok.xai")
	if err := svc.restoreCheckpointQuiet(ctx, cp); err != nil {
		t.Fatalf("restoreCheckpointQuiet: %v", err)
	}
	if _, err := os.Stat(cp.Directory); !os.IsNotExist(err) {
		t.Fatalf("checkpoint dir should be removed after restoreCheckpointQuiet")
	}
	raw, _ := os.ReadFile(os.Getenv("GROK_AUTH_PATH"))
	if !contains(string(raw), "cp@grok.xai") {
		t.Fatalf("live not restored: %s", raw)
	}
}

func TestUseRefreshesPreviousWhenLivePresent(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	seedGrokLive(t, "a@grok.xai")
	if _, err := svc.Save(ctx, "grok", "acc-a", false); err != nil {
		t.Fatal(err)
	}
	seedGrokLive(t, "b@grok.xai")
	if _, err := svc.Save(ctx, "grok", "acc-b", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveContext(context.Background(), "grok", "acc-a"); err != nil {
		t.Fatal(err)
	}
	seedGrokLive(t, "a-refreshed@grok.xai")
	res, err := svc.Use(ctx, "grok", "acc-b", UseOptions{})
	if err != nil {
		t.Fatalf("Use with refresh: %v", err)
	}
	if !res.RefreshedPrevious || res.RefreshedAccount != "acc-a" {
		t.Fatalf("want RefreshedPrevious=true acc-a, got %#v", res)
	}
	got, _ := st.Get(context.Background(), "grok", "acc-a")
	if got.Meta.Identity != "a-refreshed@grok.xai" {
		t.Fatalf("acc-a not refreshed: %q", got.Meta.Identity)
	}
}

func TestUseDoesNotOverwritePreviousWithADifferentLogin(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	credPath := filepath.Join(t.TempDir(), "claude-creds.json")
	t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", credPath)

	seedClaudeLive(t, "a@x.dev", "at-a", "rt-a")
	if _, err := svc.Save(ctx, "claude", "acc-a", false); err != nil {
		t.Fatal(err)
	}
	seedClaudeLive(t, "b@x.dev", "at-b", "rt-b")
	if _, err := svc.Save(ctx, "claude", "acc-b", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveContext(context.Background(), "claude", "acc-a"); err != nil {
		t.Fatal(err)
	}
	seedClaudeLive(t, "stranger@x.dev", "at-s", "rt-s")

	res, err := svc.Use(ctx, "claude", "acc-b", UseOptions{})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	if res.RefreshedPrevious {
		t.Fatalf("different live login was written onto the account being left: %#v", res)
	}
	got, _ := st.Get(context.Background(), "claude", "acc-a")
	if got == nil || got.Meta.Identity != "a@x.dev" {
		t.Fatalf("acc-a identity = %+v, want a@x.dev", got)
	}
}
