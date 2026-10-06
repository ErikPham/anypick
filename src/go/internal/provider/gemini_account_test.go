package provider

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGeminiBackupInputAPIKeyResolvesAsNativeAccount(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")

	p := newGemini(t.TempDir())
	snapshot := t.TempDir()
	info, err := p.BackupInput("api-key", "  gemini-key  ", "global", snapshot)
	if err != nil {
		t.Fatalf("BackupInput: %v", err)
	}
	if info.CredentialKind != "native" || info.CredentialClass != "provider_api_key" {
		t.Fatalf("snapshot info = %+v", info)
	}
	raw, err := os.ReadFile(filepath.Join(snapshot, ".env"))
	if err != nil || string(raw) != "GEMINI_API_KEY=gemini-key\n" {
		t.Fatalf("snapshot .env = %q (%v)", raw, err)
	}
	up, err := p.ResolveUpstream(snapshot)
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Auth != "gemini-key" || up.AuthHeader != "x-goog-api-key" ||
		up.Gemini == nil || !up.Gemini.APIKeyMode {
		t.Fatalf("API-key upstream = %+v", up)
	}

	for _, tc := range []struct {
		name    string
		kind    string
		secret  string
		profile string
	}{
		{name: "unknown kind", kind: "oauth", secret: "x", profile: "global"},
		{name: "unknown profile", kind: "api-key", secret: "x", profile: "europe"},
		{name: "newline", kind: "api-key", secret: "x\ny", profile: "global"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.BackupInput(tc.kind, tc.secret, tc.profile, t.TempDir()); err == nil {
				t.Fatal("invalid credential input unexpectedly succeeded")
			}
		})
	}
}

func TestGeminiGoogleAPIKeyEnvFileIsDetectedAndSaved(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("export GOOGLE_API_KEY=\"google-key\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := newGemini(home)
	live, err := p.DetectLive()
	if err != nil || !live.Present || live.Details != "API key (.env)" {
		t.Fatalf("DetectLive = %+v, err=%v", live, err)
	}
	snapshot := t.TempDir()
	if _, err := p.Backup(snapshot); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	up, err := p.ResolveUpstream(snapshot)
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Auth != "google-key" || up.AuthHeader != "x-goog-api-key" {
		t.Fatalf("upstream = %+v, want GOOGLE_API_KEY value", up)
	}
}

func TestGeminiBackupSourceSelectsOneOAuthStore(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")

	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	flat := `{"access_token":"cli-access","refresh_token":"cli-refresh","token_type":"Bearer","expiry_date":` +
		formatUnixMilli(time.Now().Add(time.Hour)) + `}`
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(flat), 0o600); err != nil {
		t.Fatal(err)
	}
	antigravityTokenFile(t, dir, "antigravity_oauth.json", "anti@example.com")

	p := newGemini(home)
	cliLive, err := p.DetectLiveSource(geminiCliOAuthSource)
	if err != nil || !cliLive.Present {
		t.Fatalf("gemini-cli source = %+v err=%v", cliLive, err)
	}
	if cliLive.Source != geminiCliOAuthSource {
		t.Fatalf("gemini-cli source id = %q", cliLive.Source)
	}
	antiLive, err := p.DetectLiveSource(antigravityOAuthSource)
	if err != nil || !antiLive.Present || antiLive.Identity != "anti@example.com" {
		t.Fatalf("antigravity source = %+v err=%v", antiLive, err)
	}
	if antiLive.Source != antigravityOAuthSource {
		t.Fatalf("antigravity source id = %q", antiLive.Source)
	}

	snapshot := t.TempDir()
	info, err := p.BackupSource(antigravityOAuthSource, snapshot)
	if err != nil {
		t.Fatalf("BackupSource: %v", err)
	}
	if info.Identity != "anti@example.com" {
		t.Fatalf("selected identity = %q", info.Identity)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "antigravity_oauth.json")); err != nil {
		t.Fatalf("selected Antigravity file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "oauth_creds.json")); !os.IsNotExist(err) {
		t.Fatalf("Gemini CLI file leaked into scoped snapshot: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(snapshot, ".env"))
	if err != nil || string(marker) != "GEMINI_OAUTH_SOURCE=antigravity\n" {
		t.Fatalf("source marker = %q (%v)", marker, err)
	}
	if got := geminiOAuthSourceFromSnapshot(snapshot); got != antigravityOAuthSource {
		t.Fatalf("snapshot source = %q", got)
	}

	// A corrupt gemini-cli candidate must not shadow a usable Antigravity file
	// when automatic source inference is used for old, unscoped snapshots.
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, "oauth_creds.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	antiRaw, err := os.ReadFile(filepath.Join(dir, "antigravity_oauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "antigravity_oauth.json"), antiRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := geminiOAuthSourceFromSnapshot(legacy); got != antigravityOAuthSource {
		t.Fatalf("corrupt candidate source = %q, want antigravity", got)
	}
}

func TestGeminiBackupKeepsAntigravityAsOAuthFallback(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")

	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	flat := `{"access_token":"cli-access","refresh_token":"cli-refresh","token_type":"Bearer","expiry_date":` +
		formatUnixMilli(time.Now().Add(time.Hour)) + `}`
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(flat), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAntigravityState(t, home, antigravityTokenSentinel,
		antigravityTokenInfo("anti-access", "Bearer", "anti-refresh", time.Now().Add(time.Hour)))

	snapshot := t.TempDir()
	info, err := newGemini(home).Backup(snapshot)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if info.Identity != "" {
		// The fixture's flat token has no id_token, so there is no display email;
		// the primary identity still must not be replaced by the fallback.
		t.Fatalf("primary identity = %q, want empty fixture identity", info.Identity)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "oauth_creds.json")); err != nil {
		t.Fatalf("Gemini CLI credential missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "antigravity_oauth.json")); err != nil {
		t.Fatalf("Antigravity fallback credential missing: %v", err)
	}
	up, err := geminiUpstream(snapshot)
	if err != nil {
		t.Fatalf("geminiUpstream: %v", err)
	}
	if up.Gemini == nil || up.Gemini.AltRoute == nil {
		t.Fatal("snapshot with both live sources must configure OAuth fallback")
	}
	if up.Gemini.AltRoute.CodeAssistBase.Host != "daily-cloudcode-pa.googleapis.com" {
		t.Fatalf("fallback base = %q, want Antigravity Code Assist host", up.Gemini.AltRoute.CodeAssistBase.Host)
	}
}

func TestGeminiClearLiveSourceKeepsAPIKeyAndAntigravity(t *testing.T) {
	home := t.TempDir()
	p := newGemini(home)
	dir := p.configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	envBody := "GEMINI_API_KEY=secret\nGOOGLE_CLOUD_PROJECT=vertex\nOTHER=keep\n"
	if err := os.WriteFile(p.livePath(".env"), []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.livePath("oauth_creds.json"), []byte(`{"refresh_token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.livePath("antigravity_oauth.json"), []byte(`{"refresh_token":"anti"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearLiveSource(geminiCliOAuthSource); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.livePath("oauth_creds.json")); !os.IsNotExist(err) {
		t.Fatal("cli oauth file should be cleared")
	}
	if _, err := os.Stat(p.livePath("antigravity_oauth.json")); err != nil {
		t.Fatalf("antigravity mirror removed: %v", err)
	}
	got, err := os.ReadFile(p.livePath(".env"))
	if err != nil || string(got) != envBody {
		t.Fatalf(".env = %q (%v), want preserved", got, err)
	}
	if err := p.ClearLiveSource(antigravityOAuthSource); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.livePath("antigravity_oauth.json")); err != nil {
		t.Fatalf("antigravity clear must be a no-op: %v", err)
	}
}

func TestGeminiSourceSaveDropsSnapshotKeepsAPIKey(t *testing.T) {
	p := newGemini(t.TempDir())
	snap := t.TempDir()
	if err := os.WriteFile(filepath.Join(snap, ".env"), []byte("GEMINI_API_KEY=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !p.SourceSaveDropsSnapshot(snap, geminiCliOAuthSource) {
		t.Fatal("oauth save must not replace an API-key snapshot")
	}
	oauthOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(oauthOnly, "oauth_creds.json"), []byte(`{"refresh_token":"rt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p.SourceSaveDropsSnapshot(oauthOnly, geminiCliOAuthSource) {
		t.Fatal("same-source oauth snapshot can be refreshed")
	}
}

func TestGeminiClearPolicyProtectsAntigravityState(t *testing.T) {
	p := newGemini(t.TempDir())
	if p.CanClearLive(antigravityOAuthSource) {
		t.Fatal("Antigravity external state must not advertise sign-out")
	}
	if !p.CanClearLive(geminiCliOAuthSource) || !p.CanClearLive("") {
		t.Fatal("Gemini-owned stores should remain clearable")
	}
}

func TestGeminiBackupRejectsMetadataWithoutUsableCredential(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")

	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// These are all plausible leftovers after a failed login, but none is
	// usable auth. Backup must not create an account that later starts empty.
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("GEMINI_OAUTH_SOURCE=antigravity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "google_accounts.json"),
		[]byte(`[{"email":"leftover@example.com"}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := newGemini(home).Backup(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "No usable live Gemini") {
		t.Fatalf("Backup error = %v, want no-live-auth failure", err)
	}
}

func TestGeminiDescribeSnapshotExtractsIDTokenEmail(t *testing.T) {
	p := newGemini(t.TempDir())
	snapshot := t.TempDir()

	// Header: {"alg":"RS256"} -> eyJhbGciOiJSUzI1NiJ9
	// Payload: {"email":"user@example.com"} -> eyJlbWFpbCI6InVzZXJAZXhhbXBsZS5jb20ifQ
	fakeJWT := "eyJhbGciOiJSUzI1NiJ9.eyJlbWFpbCI6InVzZXJAZXhhbXBsZS5jb20ifQ.signature"
	credJSON := `{"auth_method":"","email":"","id_token":"` + fakeJWT + `","token":{"access_token":"ya29.x","refresh_token":"1//x"}}`
	if err := os.WriteFile(filepath.Join(snapshot, "antigravity_oauth.json"), []byte(credJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := p.DescribeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("DescribeSnapshot: %v", err)
	}
	if info.Identity != "user@example.com" {
		t.Fatalf("info.Identity = %q, want user@example.com", info.Identity)
	}
}

func formatUnixMilli(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
