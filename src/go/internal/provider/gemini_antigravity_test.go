package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Antigravity leaves its login in a nested envelope ({"token":{…}}) under one
// of two file names, and it is the only Gemini login on machines that never
// installed gemini-cli. These tests pin the three places that used to assume
// the flat oauth_creds.json: live detection, backup, and upstream resolution.

func antigravityTokenFile(t *testing.T, dir, name, email string) string {
	t.Helper()
	body := map[string]any{
		"token": map[string]any{
			"access_token":  "ya29.access",
			"refresh_token": "1//refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(time.Hour).Format(time.RFC3339Nano),
		},
		"auth_method": "consumer",
		"email":       email,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGeminiDetectLiveSeesAntigravityToken(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	for _, name := range antigravityCredentialFiles {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
				t.Fatal(err)
			}
			antigravityTokenFile(t, filepath.Join(home, ".gemini"), name, "someone@example.com")
			p := newGemini(home)
			live, err := p.DetectLive()
			if err != nil {
				t.Fatal(err)
			}
			if !live.Present {
				t.Fatalf("live auth not detected for %s", name)
			}
			if live.Identity != "someone@example.com" {
				t.Fatalf("identity = %q, want someone@example.com", live.Identity)
			}
		})
	}
}

func TestGeminiBackupCarriesTheAntigravityToken(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	home := t.TempDir()
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := antigravityTokenFile(t, geminiDir, "jetski-standalone-oauth-token", "saved@example.com")

	p := newGemini(home)
	dest := t.TempDir()
	info, err := p.Backup(dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.Identity != "saved@example.com" {
		t.Fatalf("identity = %q", info.Identity)
	}
	copied, err := os.ReadFile(filepath.Join(dest, filepath.Base(live)))
	if err != nil {
		t.Fatalf("snapshot missing the credential: %v", err)
	}
	want, _ := os.ReadFile(live)
	if string(copied) != string(want) {
		t.Fatal("snapshot credential differs from live")
	}
}

func TestGeminiUpstreamResolvesAntigravityOnlySnapshot(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	dir := t.TempDir()
	antigravityTokenFile(t, dir, "antigravity_oauth.json", "")

	// Without the fix this fails outright: the source inference fell back to
	// gemini-cli and looked for an oauth_creds.json that is not there.
	up, err := geminiUpstream(dir)
	if err != nil {
		t.Fatalf("geminiUpstream: %v", err)
	}
	if !up.TranslateGemini || up.Gemini == nil {
		t.Fatalf("expected a gemini target: %+v", up)
	}
	if up.Gemini.APIKeyMode {
		t.Fatal("antigravity credentials are OAuth, not API-key")
	}
	if got := up.BaseURL.Host; got != "daily-cloudcode-pa.googleapis.com" {
		t.Fatalf("base = %q, want the antigravity Code Assist host", got)
	}
	if up.Gemini.IDEType != "ANTIGRAVITY" {
		t.Fatalf("ideType = %q, want ANTIGRAVITY", up.Gemini.IDEType)
	}
	if up.Gemini.UserAgent == "" {
		t.Fatal("antigravity egress needs the client User-Agent Code Assist gates on")
	}
	if up.AuthProvider == nil {
		t.Fatal("expected a per-request auth provider")
	}
}

func TestGeminiSourceInferencePrefersGeminiCLIWhenBothExist(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	dir := t.TempDir()
	antigravityTokenFile(t, dir, "antigravity_oauth.json", "")
	flat, _ := json.Marshal(map[string]any{
		"access_token": "ya29.cli", "refresh_token": "1//cli", "token_type": "Bearer",
		"expiry_date": time.Now().Add(time.Hour).UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), flat, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := geminiOAuthSourceFromSnapshot(dir); got != geminiCliOAuthSource {
		t.Fatalf("source = %q, want gemini-cli when both credentials exist", got)
	}
}

// A rotated token must be written back in the shape the file already used:
// converting an Antigravity envelope into a flat gemini-cli document leaves
// the app that owns the login unable to read it.
func TestGeminiWriteBackPreservesTheNestedShape(t *testing.T) {
	dir := t.TempDir()
	path := antigravityTokenFile(t, dir, "antigravity_oauth.json", "keep@example.com")
	c, err := loadGeminiOAuthCredentials(path, antigravityOAuthSource)
	if err != nil {
		t.Fatalf("load nested: %v", err)
	}
	if c.AccessToken != "ya29.access" || c.RefreshToken != "1//refresh" {
		t.Fatalf("nested fields not parsed: %+v", c)
	}
	if c.ExpiryDate == nil {
		t.Fatal("expiry not converted from the RFC3339 form")
	}
	c.AccessToken = "ya29.rotated"
	if err := writeGeminiOAuthCredentials(path, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["token"]; !ok {
		t.Fatalf("nested envelope lost: %s", raw)
	}
	if _, ok := doc["auth_method"]; !ok {
		t.Fatalf("unowned keys dropped: %s", raw)
	}
	reloaded, err := loadGeminiOAuthCredentials(path, antigravityOAuthSource)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.AccessToken != "ya29.rotated" {
		t.Fatalf("rotation not persisted: %q", reloaded.AccessToken)
	}
	var env geminiAntigravityFile
	_ = json.Unmarshal(raw, &env)
	if env.Email != "keep@example.com" {
		t.Fatalf("email lost on rewrite: %q", env.Email)
	}
}

func TestGeminiRejectsACredentialFileWithNoToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "antigravity_oauth.json")
	if err := os.WriteFile(path, []byte(`{"auth_method":"consumer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGeminiOAuthCredentials(path, antigravityOAuthSource); err == nil {
		t.Fatal("a credential file with no token must not load")
	}
}
