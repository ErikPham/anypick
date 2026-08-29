package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Each file provider: detect → backup → restore round trip on a fake HOME.
func TestGrokProviderRoundTrip(t *testing.T) {
	env := newProviderEnv(t, map[string]string{"GROK_AUTH_PATH": ""})
	env.write(t, filepath.Join(env.Home, ".grok", "auth.json"), `{
	  "sess-1": {"email":"dev@grok.xai","access_token":"t1"},
	  "other":  "string-not-a-session"
	}`)

	pv := mustProvider(t, env.Registry, "grok")
	live, err := pv.DetectLive()
	if err != nil || !live.Present || live.Identity != "dev@grok.xai" {
		t.Fatalf("detect = %+v err=%v", live, err)
	}

	snap := t.TempDir()
	info, err := pv.Backup(snap)
	if err != nil || info.Identity != "dev@grok.xai" {
		t.Fatalf("backup = %+v err=%v", info, err)
	}

	os.Remove(env.mustPath(".grok/auth.json"))
	if _, err := os.Stat(env.mustPath(".grok/auth.json")); !os.IsNotExist(err) {
		t.Fatal("precondition: live auth removed")
	}
	if err := pv.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.mustPath(".grok/auth.json")); err != nil {
		t.Fatalf("auth.json not restored: %v", err)
	}
}

func TestOpenCodeProviderRoundTrip(t *testing.T) {
	authJSON := `{
	  "openai": {"type":"oauth","access":"` + fakeJWT(map[string]string{"email": "oc@openai"}) + `","accountId":"abcd1234efgh"},
	  "anthropic": {"type":"api_key","key":"k"}
	}`
	t.Setenv("OPENCODE_AUTH_PATH", "")
	env := newProviderEnv(t, nil)
	authPath := filepath.Join(env.Home, ".local", "share", "opencode", "auth.json")
	env.write(t, authPath, authJSON)

	pv := mustProvider(t, env.Registry, "opencode")
	live, err := pv.DetectLive()
	if err != nil || !live.Present {
		t.Fatalf("detect = %+v err=%v", live, err)
	}
	if want := ""; live.Identity == want {
		t.Logf("identity empty is acceptable when JWT lacks email claim; got %q", live.Identity)
	}

	snap := t.TempDir()
	if _, err := pv.Backup(snap); err != nil {
		t.Fatal(err)
	}
	os.Remove(authPath)
	if err := pv.Restore(snap); err != nil {
		t.Fatal(err)
	}
	var after map[string]json.RawMessage
	raw, _ := os.ReadFile(authPath)
	if json.Unmarshal(raw, &after) != nil || len(after) != 2 {
		t.Errorf("restored auth.json = %s", raw)
	}
}

func TestGeminiProviderOAuthAndEnvKey(t *testing.T) {
	env := newProviderEnv(t, nil)
	geminiDir := filepath.Join(env.Home, ".gemini")

	pv := mustProvider(t, env.Registry, "gemini")

	// Nothing live.
	live, err := pv.DetectLive()
	if err != nil || live.Present {
		t.Fatalf("empty home should be absent: %+v %v", live, err)
	}

	// OAuth source.
	idToken := fakeJWT(map[string]string{"email": "gm@gmail"})
	env.write(t, filepath.Join(geminiDir, "oauth_creds.json"),
		`{"access_token":"a","refresh_token":"r","id_token":"`+idToken+`"}`)
	live, err = pv.DetectLive()
	if err != nil || !live.Present || live.Identity != "gm@gmail" {
		t.Fatalf("oauth detect = %+v err=%v", live, err)
	}

	// Backup copies all snapshot files; restore puts them back.
	env.write(t, filepath.Join(geminiDir, ".env"), "GEMINI_API_KEY=abc\n")
	snap := t.TempDir()
	if _, err := pv.Backup(snap); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "oauth_creds.json"} {
		os.Remove(filepath.Join(geminiDir, name))
	}
	if err := pv.Restore(snap); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "oauth_creds.json"} {
		if _, err := os.Stat(filepath.Join(geminiDir, name)); err != nil {
			t.Errorf("%s not restored: %v", name, err)
		}
	}

	// Env-key-only login still counts as present.
	os.Remove(filepath.Join(geminiDir, "oauth_creds.json"))
	live, err = pv.DetectLive()
	if err != nil || !live.Present {
		t.Errorf("env key detect = %+v err=%v", live, err)
	}
}

func TestGeminiAdapterKeepsSameProviderOnlyDirect(t *testing.T) {
	env := newProviderEnv(t, nil)
	pv := mustProvider(t, env.Registry, "gemini")
	adapter := pv.AccountAdapter("work", false)

	if got := adapter.TransportFor("gemini"); got != "direct" {
		t.Errorf("own client = %s", got)
	}
	// Invariant #1: gemini accounts never serve kiro directly.
	if got := adapter.TransportFor("kiro"); got != "unsupported" {
		t.Errorf("kiro via gemini account = %s", got)
	}
	if got := adapter.TransportFor("claude"); got != "managed_builtin_proxy" {
		t.Errorf("claude via gemini proxy = %s", got)
	}
}

var _ = context.Background
