// LiveClearer implementations for grok, codex, opencode, and gemini.
// Clearing is local-only: server-side OAuth revocation is never attempted.
package provider

import (
	"os"

	"github.com/outof0/anypick/go/internal/atomicfile"
)

func removeQuiet(paths ...string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// ClearLive deletes local Grok auth.json only — it does NOT revoke OIDC
// tokens server-side.
func (p *grok) ClearLive() error {
	removeQuiet(p.authPath(), p.authPath()+".lock")
	return nil
}

// ClearLive removes auth.json so Codex treats this machine as signed out.
// Saved AnyPick snapshots remain valid for restore.
func (p *codex) ClearLive() error {
	path := p.authPath()
	removeQuiet(path, path+".lock")
	return nil
}

// ClearLive forgets the local OpenCode login; saved snapshots stay valid.
func (p *opencode) ClearLive() error {
	removeQuiet(p.authPath())
	return nil
}

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
// and returns the remaining body.
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

// ClearLiveSource removes only the credential files the selected sign-in is
// about to rewrite. Antigravity's IDE store is not AnyPick's to clear
// (CanClearLive), and a Gemini CLI login must not delete the Antigravity
// mirror or the API-key and Vertex lines in .env — those are a different
// credential the new OAuth snapshot does not replace.
func (p *gemini) ClearLiveSource(source string) error {
	if !p.CanClearLive(source) {
		return nil
	}
	for _, name := range geminiCredentialFilesFor(source) {
		removeQuiet(p.livePath(name))
	}
	if source == geminiCliOAuthSource || source == "" {
		removeQuiet(p.livePath("google_accounts.json"))
	}
	return nil
}

// ClearLive drops the Gemini CLI credential files and strips auth keys from
// .env without touching OS keychain entries or revoking OAuth server-side.
// Sign-out uses this. Browser login uses ClearLiveSource so it cannot discard
// a credential the new snapshot does not replace.
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
	// envPath is the live ~/.gemini/.env (ADR 0004): rename, not truncate.
	return atomicfile.Write(envPath, []byte(body), 0o600)
}
