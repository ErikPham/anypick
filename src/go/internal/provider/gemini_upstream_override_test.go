package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeminiUpstreamOverridesRefuseOffLoopback(t *testing.T) {
	t.Setenv("GEMINI_CODE_ASSIST_UPSTREAM", "https://evil.example")
	if _, err := geminiCodeAssistBase(geminiCliOAuthSource); err == nil {
		t.Fatal("off-loopback code assist override was honored")
	}
	t.Setenv("GEMINI_API_KEY_UPSTREAM", "https://evil.example/v1")
	if _, err := geminiKeyUpstream("secret-key"); err == nil {
		t.Fatal("off-loopback API key override was honored")
	}

	t.Setenv("GEMINI_CODE_ASSIST_UPSTREAM", "http://127.0.0.1:9")
	base, err := geminiCodeAssistBase(geminiCliOAuthSource)
	if err != nil || base == nil || base.Host != "127.0.0.1:9" {
		t.Fatalf("loopback code assist override: %v %v", base, err)
	}
	t.Setenv("GEMINI_API_KEY_UPSTREAM", "http://localhost:9")
	up, err := geminiKeyUpstream("secret-key")
	if err != nil || up == nil || up.BaseURL.Host != "localhost:9" {
		t.Fatalf("loopback API key override: %+v %v", up, err)
	}
}

func TestGeminiProcessEnvKeyDoesNotReplaceSavedAccount(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "AIza-other")
	t.Setenv("GOOGLE_API_KEY", "AIza-google-other")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	t.Setenv("GEMINI_CODE_ASSIST_UPSTREAM", "")
	t.Setenv("GEMINI_API_KEY_UPSTREAM", "")

	snap := t.TempDir()
	if err := os.WriteFile(filepath.Join(snap, "oauth_creds.json"), []byte(
		`{"access_token":"cli-access","refresh_token":"cli-refresh","token_type":"Bearer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newGemini(t.TempDir())
	up, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	if up.AuthHeader == "x-goog-api-key" || up.Auth == "AIza-other" || up.Auth == "AIza-google-other" ||
		(up.Gemini != nil && up.Gemini.APIKeyMode) {
		t.Fatalf("process env replaced the oauth snapshot: header %q auth %q", up.AuthHeader, up.Auth)
	}

	if err := os.WriteFile(filepath.Join(snap, ".env"), []byte("GEMINI_API_KEY=snapshot-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyed, err := p.ResolveUpstream(snap)
	if err != nil {
		t.Fatal(err)
	}
	if keyed.Auth != "snapshot-key" || keyed.AuthHeader != "x-goog-api-key" {
		t.Fatalf("snapshot key = header %q auth %q", keyed.AuthHeader, keyed.Auth)
	}
}
