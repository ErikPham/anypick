package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/transport"
)

// gemini mirrors the gemini-cli account source of GeminiProvider. The
// Antigravity keychain source stays with the host composition (deferred).
type gemini struct{ home string }

func newGemini(home string) *gemini { return &gemini{home: home} }

var geminiSnapshotFiles = []string{".env", "oauth_creds.json", "google_accounts.json"}

func (p *gemini) ID() string          { return "gemini" }
func (p *gemini) Name() string        { return "Google Gemini" }
func (p *gemini) Description() string { return "Manages ~/.gemini (oauth_creds.json, .env)" }
func (p *gemini) CanProxy() bool      { return true }

// AccountAdapter keeps invariant #1: direct ONLY for the gemini client; other
// clients ride the builtin proxy.
func (p *gemini) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.GeminiAccountAdapter(p.ID(), accountName)
}

func (p *gemini) configDir() string {
	if env := os.Getenv("GEMINI_CONFIG_DIR"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(expandHome(p.home), ".gemini")
}

func (p *gemini) livePath(name string) string { return filepath.Join(p.configDir(), name) }

func (p *gemini) hasOAuth() bool {
	raw, err := os.ReadFile(p.livePath("oauth_creds.json"))
	if err != nil {
		return false
	}
	var creds map[string]json.RawMessage
	if json.Unmarshal(raw, &creds) != nil {
		return false
	}
	if _, ok := creds["refresh_token"]; ok {
		return true
	}
	_, ok := creds["access_token"]
	return ok
}

func (p *gemini) hasEnvAPIKey() bool {
	data, err := os.ReadFile(p.livePath(".env"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "GEMINI_API_KEY=") && len(line) > len("GEMINI_API_KEY=") {
			return true
		}
	}
	return false
}

func (p *gemini) DetectLive() (LiveAuthStatus, error) {
	switch {
	case p.hasOAuth():
		return LiveAuthStatus{
			Present:  true,
			Identity: p.oauthEmail(),
			Details:  "gemini-cli oauth",
		}, nil
	case p.hasEnvAPIKey():
		return LiveAuthStatus{Present: true, Details: "API key (.env GEMINI_API_KEY)"}, nil
	default:
		return LiveAuthStatus{}, nil
	}
}

func (p *gemini) oauthEmail() string {
	raw, err := os.ReadFile(p.livePath("oauth_creds.json"))
	if err != nil {
		return ""
	}
	var creds struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(raw, &creds) == nil && creds.IDToken != "" {
		if email := jwtEmail(creds.IDToken); email != "" {
			return email
		}
	}
	// google_accounts.json fallback.
	acctRaw, err := os.ReadFile(p.livePath("google_accounts.json"))
	if err != nil {
		return ""
	}
	var accounts []struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(acctRaw, &accounts) == nil && len(accounts) > 0 &&
		strings.Contains(accounts[0].Email, "@") {
		return accounts[0].Email
	}
	return ""
}

func (p *gemini) Backup(destDir string) (SnapshotInfo, error) {
	found := false
	info := SnapshotInfo{}
	for _, name := range geminiSnapshotFiles {
		src := p.livePath(name)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		found = true
		if err := os.WriteFile(filepath.Join(destDir, name), data, 0o600); err != nil {
			return SnapshotInfo{}, err
		}
	}
	if !found {
		return SnapshotInfo{}, anyerr.Newf(
			"No live Gemini CLI auth found under "+p.configDir()+". Log in first, then save.",
			"NO_LIVE_AUTH",
			anyerr.WithSuggestions("Run `gemini` and sign in, then try again."))
	}
	if p.hasOAuth() {
		info.Identity = p.oauthEmail()
	} else if p.hasEnvAPIKey() {
		info.Notes = "API key"
		info.CredentialKind = "native"
	}
	return info, nil
}

func (p *gemini) Restore(srcDir string) error {
	restored := false
	for _, name := range geminiSnapshotFiles {
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			continue
		}
		dest := p.livePath(name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return err
		}
		restored = true
	}
	if !restored {
		return errSnapshotMissing("any Gemini CLI credential file")
	}
	return nil
}

func (p *gemini) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	info := SnapshotInfo{}
	for _, name := range geminiSnapshotFiles {
		if _, err := os.Stat(filepath.Join(srcDir, name)); err == nil {
			info.CredentialKind = "native"
			break
		}
	}
	return info, nil
}

func (gemini *gemini) ShortName() string { return "Gemini" }

// ResolveUpstream points the engine's shared proxy at Google Gemini: API-key
// or OAuth/Code Assist, both translated through serveGemini
// (OpenAI/Anthropic/Responses → generateContent → back). Ports the gemini-cli
// auth-source half of gemini-proxy/server.ts.
func (p *gemini) ResolveUpstream(snapshotDir string) (*proxy.Upstream, error) {
	return geminiUpstream(snapshotDir)
}
