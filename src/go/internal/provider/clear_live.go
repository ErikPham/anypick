// LiveClearer implementations missing from the per-provider ports
// (clearLive in src/providers/{grok,codex,opencode,gemini}.ts). Clearing is
// local-only: server-side OAuth revocation is never attempted.
package provider

import "os"

func removeQuiet(paths ...string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// ClearLive deletes local Grok auth.json only — it does NOT revoke OIDC
// tokens server-side (mirrors GrokProvider.clearLive).
func (p *grok) ClearLive() error {
	removeQuiet(p.authPath(), p.authPath()+".lock")
	return nil
}

// ClearLive removes auth.json so Codex treats this machine as signed out.
// Saved AnyPick snapshots remain valid for restore (CodexProvider.clearLive).
func (p *codex) ClearLive() error {
	path := p.authPath()
	removeQuiet(path, path+".lock")
	return nil
}

// ClearLive forgets the local OpenCode login; saved snapshots stay valid
// (OpenCodeProvider.clearLive).
func (p *opencode) ClearLive() error {
	removeQuiet(p.authPath())
	return nil
}

// geminiAuthEnvKeys mirrors AUTH_ENV_KEYS in src/providers/gemini-env.ts.
var geminiAuthEnvKeys = map[string]bool{
	"GEMINI_API_KEY":                 true,
	"GOOGLE_API_KEY":                 true,
	"GOOGLE_APPLICATION_CREDENTIALS": true,
	"GOOGLE_CLOUD_PROJECT":           true,
	"GOOGLE_CLOUD_PROJECT_ID":        true,
	"GOOGLE_CLOUD_LOCATION":          true,
	"GOOGLE_GENAI_USE_VERTEXAI":      true,
}

// stripEnvAuthKeys removes known Gemini auth assignments from a dotenv file
// and returns the remaining body ("port of stripEnvAuthKeys").
func stripEnvAuthKeys(raw string) string {
	kept := make([]string, 0, 16)
	for _, line := range splitLines(raw) {
		if key, ok := envLineKey(line); ok && geminiAuthEnvKeys[key] {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && kept[len(kept)-1] == "" {
		kept = kept[:len(kept)-1]
	}
	return joinLines(kept)
}

// ClearLive drops the Gemini CLI credential files and strips auth keys from
// .env without touching OS keychain entries or revoking OAuth server-side.
func (p *gemini) ClearLive() error {
	// Every name a Gemini login may use, not just oauth_creds.json: on an
	// Antigravity machine that file is absent and clearing only it would
	// report success while leaving the machine signed in.
	for _, name := range geminiAllCredentialFiles() {
		removeQuiet(p.livePath(name))
	}
	removeQuiet(p.livePath("google_accounts.json"))
	envPath := p.livePath(".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return nil // absent or unreadable: nothing to clear
	}
	next := stripEnvAuthKeys(string(raw))
	if next == "" {
		_ = os.Remove(envPath)
		return nil
	}
	body := next
	if body[len(body)-1] != '\n' {
		body += "\n"
	}
	return os.WriteFile(envPath, []byte(body), 0o600)
}
