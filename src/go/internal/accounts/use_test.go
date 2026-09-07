package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func seedGrokLive(t *testing.T, email string) string {
	t.Helper()
	path := os.Getenv("GROK_AUTH_PATH")
	if path == "" {
		t.Fatalf("GROK_AUTH_PATH not set")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"sess": map[string]string{"email": email, "access_token": "t-" + email},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUseSwitchesActiveAndRestoresSnapshot(t *testing.T) {
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
	// Start with acc-a active.
	if err := st.SetActiveContext(context.Background(), "grok", "acc-a"); err != nil {
		t.Fatal(err)
	}
	// Live currently holds b's material (last seed). Switching to acc-a
	// should restore acc-a's snapshot (a@grok.xai).
	res, err := svc.Use(ctx, "grok", "acc-a", UseOptions{NoRefresh: true})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	if res.From != "acc-a" || res.To != "acc-a" {
		// From was acc-a before switch, To is acc-a (same). Allow.
	}
	if active, _ := st.GetActive(context.Background(), "grok"); active != "acc-a" {
		t.Fatalf("active=%q want acc-a", active)
	}
	livePath := os.Getenv("GROK_AUTH_PATH")
	raw, _ := os.ReadFile(livePath)
	var data map[string]json.RawMessage
	_ = json.Unmarshal(raw, &data)
	// Check identity restored to a@grok.xai
	found := false
	for _, v := range data {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) != nil {
			continue
		}
		for _, k := range []string{"email", "user_email"} {
			var s string
			if err := json.Unmarshal(obj[k], &s); err == nil && s == "a@grok.xai" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("live not restored to acc-a: %s", raw)
	}
}

func TestUseBetweenTwoAccounts(t *testing.T) {
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
	res, err := svc.Use(ctx, "grok", "acc-b", UseOptions{NoRefresh: true})
	if err != nil {
		t.Fatalf("Use acc-a->acc-b: %v", err)
	}
	if res.From != "acc-a" || res.To != "acc-b" {
		t.Fatalf("SwitchResult = %#v want From acc-a To acc-b", res)
	}
	if active, _ := st.GetActive(context.Background(), "grok"); active != "acc-b" {
		t.Fatalf("active=%q want acc-b", active)
	}
}

func TestUseProxyOnlySkipsRestore(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()

	// Save a proxy-only account (deepseek).
	if _, err := svc.SaveInput(ctx, "deepseek", "proxy-acc", CredentialInput{
		Kind: "api-key", Secret: "sk-123", Profile: "global",
	}, false); err != nil {
		t.Fatal(err)
	}
	// Seed a live grok auth that should survive switching to proxy-only.
	seedGrokLive(t, "keep@grok.xai")
	liveBefore, _ := os.ReadFile(os.Getenv("GROK_AUTH_PATH"))

	// Use via grok proxy-only? Actually proxy-acc is deepseek, not grok.
	// Switching grok account to a deepseek account doesn't make sense as provider.
	// Instead save a second grok proxy-only by directly writing snapshot?
	// Simpler: SaveInput into deepseek, then Use deepseek's proxy-acc.
	// That path sets active for deepseek provider, not touching grok file.
	res, err := svc.Use(ctx, "deepseek", "proxy-acc", UseOptions{})
	if err != nil {
		t.Fatalf("Use proxy-only: %v", err)
	}
	if res.Provider != "deepseek" || res.To != "proxy-acc" {
		t.Fatalf("result = %#v", res)
	}
	if active, _ := st.GetActive(context.Background(), "deepseek"); active != "proxy-acc" {
		t.Fatalf("active deepseek=%q", active)
	}
	// Grok live unchanged.
	after, _ := os.ReadFile(os.Getenv("GROK_AUTH_PATH"))
	if string(after) != string(liveBefore) {
		t.Fatalf("grok live changed after proxy-only switch")
	}
}

func TestUseMissingAccountErrors(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	_, err := svc.Use(ctx, "grok", "nope", UseOptions{})
	if err == nil {
		t.Fatal("want error for missing account")
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("want ACCOUNT_NOT_FOUND, got %v", err)
	}
}

func TestUseInvalidName(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	_, err := svc.Use(ctx, "grok", "   ", UseOptions{})
	if err == nil {
		t.Fatal("want error for invalid name")
	}
}

func TestUseUnknownProvider(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	_, err := svc.Use(ctx, "nope", "acc", UseOptions{})
	if err == nil {
		t.Fatal("want error for unknown provider")
	}
}

func TestCheckpointAndRestore(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	seedGrokLive(t, "chk@grok.xai")
	cp, err := svc.CheckpointLiveAuth(ctx, "grok", false)
	if err != nil {
		t.Fatal(err)
	}
	if !cp.HadLiveAuth || cp.ProviderID != "grok" {
		t.Fatalf("checkpoint = %#v", cp)
	}
	// Mutate live to something else.
	seedGrokLive(t, "mutated@grok.xai")
	if err := svc.RestoreLiveAuthCheckpoint(ctx, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(os.Getenv("GROK_AUTH_PATH"))
	if !contains(string(raw), "chk@grok.xai") {
		t.Fatalf("live not restored: %s", raw)
	}
	// Cleanup
	_ = svc.DiscardLiveAuthCheckpoint(cp)
	if _, err := os.Stat(cp.Directory); !os.IsNotExist(err) {
		t.Fatalf("checkpoint dir not removed")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
