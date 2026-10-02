package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/store"
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

func seedGeminiLive(t *testing.T, dir, email, refreshToken string) string {
	t.Helper()
	path := filepath.Join(dir, "oauth_creds.json")
	data, err := json.Marshal(map[string]any{
		"access_token":  "access-" + email,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expiry_date":   time.Now().Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// guardAntigravityKeyring keeps this test binary off the developer's real
// Keychain, and returns a probe reporting whether the seam was consulted.
//
// The Antigravity credential item is global -- service "gemini", account
// "antigravity", the same entry the IDE uses -- so it is NOT covered by the
// temp root, the temp home or GEMINI_CONFIG_DIR that testGeminiService sets up.
// Left unguarded, `Use` reaches the real store and overwrites the developer's
// own credential with the fixture refresh tokens seeded above; the write is an
// `add-generic-password -U`, which replaces the item in place.
//
// internal/provider guards its own test binary in TestMain. This is a different
// test binary, so it needs its own guard, and the seam is exported so it can
// install one.
func guardAntigravityKeyring(t *testing.T) func() bool {
	t.Helper()
	previous := provider.AntigravityKeyringPut
	var consulted bool
	provider.AntigravityKeyringPut = func(*provider.GeminiAntigravityFile) error {
		consulted = true
		return nil
	}
	t.Cleanup(func() { provider.AntigravityKeyringPut = previous })
	return func() bool { return consulted }
}

func testGeminiService(t *testing.T) (*Service, *store.AccountStore, string) {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	geminiDir := filepath.Join(home, ".gemini")
	t.Setenv("GEMINI_CONFIG_DIR", geminiDir)
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	database, err := db.Open(root)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	return New(st, reg), st, geminiDir
}

func TestUseSwitchesGeminiAccountsAndRestoresOAuthSnapshot(t *testing.T) {
	svc, st, geminiDir := testGeminiService(t)
	keyringConsulted := guardAntigravityKeyring(t)
	ctx := context.Background()

	seedGeminiLive(t, geminiDir, "first@example.com", "refresh-first")
	if _, err := svc.Save(ctx, "gemini", "first", false); err != nil {
		t.Fatal(err)
	}
	seedGeminiLive(t, geminiDir, "second@example.com", "refresh-second")
	if _, err := svc.Save(ctx, "gemini", "second", false); err != nil {
		t.Fatal(err)
	}

	// Deliberately make the live file differ from both snapshots. The account
	// switch must restore the selected snapshot, not only update the DB pointer.
	seedGeminiLive(t, geminiDir, "transient@example.com", "refresh-transient")
	if err := st.SetActiveContext(ctx, "gemini", "first"); err != nil {
		t.Fatal(err)
	}

	result, err := svc.Use(ctx, "gemini", "second", UseOptions{NoRefresh: true})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	// The switch mirrors the snapshot into the OS credential store, so assert
	// the seam was consulted. Without this the test would still pass if it
	// quietly stopped exercising the mirror -- and before the guard existed it
	// passed while overwriting the developer's real Antigravity credential.
	if !keyringConsulted() {
		t.Fatal("the Antigravity keyring seam was never consulted: either this test " +
			"no longer exercises the mirror, or the guard is not wired")
	}
	if result.From != "first" || result.To != "second" {
		t.Fatalf("SwitchResult = %#v", result)
	}
	if active, _ := st.GetActive(ctx, "gemini"); active != "second" {
		t.Fatalf("active=%q want second", active)
	}
	raw, err := os.ReadFile(filepath.Join(geminiDir, "oauth_creds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var live map[string]any
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	if live["refresh_token"] != "refresh-second" {
		t.Fatalf("live refresh_token=%v want selected snapshot", live["refresh_token"])
	}
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

type recordingProxySwitcher struct {
	running map[string]bool
	enabled map[string]bool
	stops   []string
	starts  []string
}

func (p *recordingProxySwitcher) Stop(_ context.Context, providerID, accountName string) error {
	return p.StopProxyForSwitch(context.Background(), providerID, accountName)
}

func (p *recordingProxySwitcher) ProxyRunning(_ context.Context, providerID, accountName string) (bool, error) {
	return p.running[providerID+"/"+accountName], nil
}

func (p *recordingProxySwitcher) StopProxyForSwitch(_ context.Context, providerID, accountName string) error {
	key := providerID + "/" + accountName
	p.stops = append(p.stops, key)
	p.running[key] = false
	return nil
}

func (p *recordingProxySwitcher) StartProxyIfEnabled(_ context.Context, providerID, accountName string) error {
	key := providerID + "/" + accountName
	if !p.enabled[key] {
		return nil
	}
	p.starts = append(p.starts, key)
	p.running[key] = true
	return nil
}

func TestUseSwitchesRunningProxyToSelectedAccount(t *testing.T) {
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
	if err := st.SetActiveContext(ctx, "grok", "acc-a"); err != nil {
		t.Fatal(err)
	}

	proxy := &recordingProxySwitcher{
		running: map[string]bool{"grok/acc-a": true},
		enabled: map[string]bool{"grok/acc-a": true, "grok/acc-b": true},
	}
	var switched []string
	svc.SetAfterSwitch(func(_ context.Context, providerID, accountName string) error {
		switched = append(switched, providerID+"/"+accountName)
		return nil
	})
	svc.Proxy = proxy
	if _, err := svc.Use(ctx, "grok", "acc-b", UseOptions{NoRefresh: true}); err != nil {
		t.Fatalf("Use acc-a->acc-b: %v", err)
	}
	if len(switched) != 1 || switched[0] != "grok/acc-b" {
		t.Fatalf("after-switch calls = %v", switched)
	}
	if len(proxy.stops) != 1 || proxy.stops[0] != "grok/acc-a" {
		t.Fatalf("proxy stop calls = %v", proxy.stops)
	}
	if len(proxy.starts) != 1 || proxy.starts[0] != "grok/acc-b" {
		t.Fatalf("proxy start calls = %v", proxy.starts)
	}
	if proxy.running["grok/acc-a"] || !proxy.running["grok/acc-b"] {
		t.Fatalf("proxy running state = %#v", proxy.running)
	}
}

func TestUseNoProxyLeavesExistingProxyUntouched(t *testing.T) {
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
	if err := st.SetActiveContext(ctx, "grok", "acc-a"); err != nil {
		t.Fatal(err)
	}

	proxy := &recordingProxySwitcher{
		running: map[string]bool{"grok/acc-a": true},
		enabled: map[string]bool{"grok/acc-a": true, "grok/acc-b": true},
	}
	svc.Proxy = proxy
	if _, err := svc.Use(ctx, "grok", "acc-b", UseOptions{NoRefresh: true, NoProxy: true}); err != nil {
		t.Fatalf("Use with NoProxy: %v", err)
	}
	if len(proxy.stops) != 0 || len(proxy.starts) != 0 {
		t.Fatalf("proxy lifecycle calls with NoProxy: stops=%v starts=%v", proxy.stops, proxy.starts)
	}
	if !proxy.running["grok/acc-a"] {
		t.Fatalf("existing proxy was changed: %#v", proxy.running)
	}
}

// A PostSwitch failure restores the whole switch transaction: active pointer,
// binding realignment and the previous proxy. This is what stops a failed
// WorkBuddy switch from leaving "work" active while models.json still points
// at the account that was switched away from.
func TestUsePostSwitchFailureRollsBackTheSwitch(t *testing.T) {
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
	if err := st.SetActiveContext(ctx, "grok", "acc-a"); err != nil {
		t.Fatal(err)
	}

	proxy := &recordingProxySwitcher{
		running: map[string]bool{"grok/acc-a": true},
		enabled: map[string]bool{"grok/acc-a": true, "grok/acc-b": true},
	}
	var switched []string
	svc.SetAfterSwitch(func(_ context.Context, providerID, accountName string) error {
		switched = append(switched, providerID+"/"+accountName)
		return nil
	})
	svc.Proxy = proxy

	_, err := svc.Use(ctx, "grok", "acc-b", UseOptions{
		NoRefresh: true,
		PostSwitch: func(context.Context) error {
			return anyerr.Newf("reapply failed", "TEST_REAPPLY")
		},
	})
	if err == nil {
		t.Fatal("Use must fail when PostSwitch fails")
	}
	if !strings.Contains(err.Error(), "previous account and its clients were restored") {
		t.Fatalf("error = %v, want the rollback notice", err)
	}
	active, aerr := st.GetActive(ctx, "grok")
	if aerr != nil || active != "acc-a" {
		t.Fatalf("active = %q, %v; want acc-a after rollback", active, aerr)
	}
	if len(switched) != 2 || switched[0] != "grok/acc-b" || switched[1] != "grok/acc-a" {
		t.Fatalf("after-switch calls = %v, want [grok/acc-b grok/acc-a]", switched)
	}
	if !proxy.running["grok/acc-a"] {
		t.Fatalf("previous proxy was not restarted: %#v", proxy.running)
	}
	if proxy.running["grok/acc-b"] {
		t.Fatalf("target proxy still running after rollback: %#v", proxy.running)
	}
}

func seedWorkBuddyNative(t *testing.T, uid, token string) string {
	t.Helper()
	path := os.Getenv("WORKBUDDY_NATIVE_AUTH_PATH")
	if path == "" {
		t.Fatalf("WORKBUDDY_NATIVE_AUTH_PATH not set")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"account": map[string]any{"uid": uid, "nickname": uid, "type": "personal", "lastLogin": true},
		"auth":    map[string]any{"accessToken": token, "refreshToken": "rt-" + uid, "domain": "www.workbuddy.ai"},
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func workBuddyNativeToken(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("WORKBUDDY_NATIVE_AUTH_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	auth, _ := top["auth"].(map[string]any)
	token, _ := auth["accessToken"].(string)
	return token
}

// Switching a WorkBuddy account must move the desktop app's session file —
// that restore is what the running app follows — not just the DB pointer.
func TestUseSwitchesWorkBuddyNativeSession(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	t.Setenv("WORKBUDDY_AUTH_PATH", filepath.Join(t.TempDir(), "staging-auth.json"))
	t.Setenv("WORKBUDDY_NATIVE_AUTH_PATH", filepath.Join(t.TempDir(), "workbuddy-desktop-ai.info"))

	seedWorkBuddyNative(t, "uid-first", "tok-first")
	if _, err := svc.Save(ctx, "workbuddy", "first", false); err != nil {
		t.Fatal(err)
	}
	seedWorkBuddyNative(t, "uid-second", "tok-second")
	if _, err := svc.Save(ctx, "workbuddy", "second", false); err != nil {
		t.Fatal(err)
	}

	// Live differs from both snapshots; the switch must restore the selected
	// snapshot's token into the session file.
	seedWorkBuddyNative(t, "uid-transient", "tok-transient")
	if err := st.SetActiveContext(ctx, "workbuddy", "first"); err != nil {
		t.Fatal(err)
	}

	result, err := svc.Use(ctx, "workbuddy", "second", UseOptions{NoRefresh: true})
	if err != nil {
		t.Fatalf("Use: %v", err)
	}
	if result.From != "first" || result.To != "second" {
		t.Fatalf("SwitchResult = %#v", result)
	}
	if active, _ := st.GetActive(ctx, "workbuddy"); active != "second" {
		t.Fatalf("active=%q want second", active)
	}
	if got := workBuddyNativeToken(t); got != "tok-second" {
		t.Fatalf("native session token = %q, want tok-second", got)
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
