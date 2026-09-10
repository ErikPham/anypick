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

// geminiSnapshotFiles is the live-login set copied by Backup and restored by
// Restore. It covers both OAuth writers: gemini-cli's flat oauth_creds.json
// and Antigravity's two interchangeable nested files (see
// geminiAllCredentialFiles). Missing names here is why an Antigravity login
// read as "no live auth" and could not be saved at all.
var geminiSnapshotFiles = append([]string{".env"}, append(
	geminiAllCredentialFiles(), "google_accounts.json")...)

func (p *gemini) ID() string   { return "gemini" }
func (p *gemini) Name() string { return "Google Gemini" }
func (p *gemini) Description() string {
	return "Manages ~/.gemini (oauth_creds.json, .env) and the Antigravity IDE login"
}
func (p *gemini) CanProxy() bool { return true }

// AccountAdapter keeps invariant #1: direct ONLY for the gemini client; other
// clients ride the builtin proxy.
func (p *gemini) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.GeminiAccountAdapter(p.ID(), accountName)
}

func (p *gemini) configDir() string {
	// Same rule as client.geminiDir, and for the same reason: GEMINI_CLI_HOME
	// is the variable @google/gemini-cli actually reads and it replaces HOME,
	// so the config dir is "$GEMINI_CLI_HOME/.gemini". GEMINI_CONFIG_DIR is
	// AnyPick's own knob and must lose to it — see the note there.
	if cliHome := os.Getenv("GEMINI_CLI_HOME"); cliHome != "" {
		return filepath.Join(expandHome(cliHome), ".gemini")
	}
	if env := os.Getenv("GEMINI_CONFIG_DIR"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(expandHome(p.home), ".gemini")
}

func (p *gemini) livePath(name string) string { return filepath.Join(p.configDir(), name) }

// oauthCredFile returns the first Gemini OAuth file holding a usable token,
// or "". It probes every known name rather than assuming oauth_creds.json,
// because which file exists is a property of the app that wrote the login,
// not of AnyPick.
func (p *gemini) oauthCredFile() string {
	for _, name := range geminiAllCredentialFiles() {
		raw, err := os.ReadFile(p.livePath(name))
		if err != nil {
			continue
		}
		if _, ok := parseGeminiOAuthCredentials(raw); ok {
			return name
		}
	}
	return ""
}

func (p *gemini) hasOAuth() bool { return p.oauthCredFile() != "" }

// antigravityStateCredential is the IDE-state login, or nil when Antigravity
// is not installed or is signed out. Read-only by construction: detecting an
// account must never touch another product's credential store.
func (p *gemini) antigravityStateCredential() *geminiAntigravityFile {
	return antigravityStateCredential(p.home)
}

// geminiOAuthSourceOfFile maps a credential file name back to the OAuth
// profile that can refresh it.
func geminiOAuthSourceOfFile(name string) string {
	for _, candidate := range antigravityCredentialFiles {
		if candidate == name {
			return antigravityOAuthSource
		}
	}
	return geminiCliOAuthSource
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
			Details:  geminiOAuthSourceOfFile(p.oauthCredFile()) + " oauth",
		}, nil
	case p.hasEnvAPIKey():
		return LiveAuthStatus{Present: true, Details: "API key (.env GEMINI_API_KEY)"}, nil
	default:
		// Last resort: Antigravity keeps its login in the IDE's own state
		// database and in the OS credential store, never under ~/.gemini.
		//
		// "Present" and "identified" are two different answers here. The
		// keyring probe lists an item's attributes without reading its secret,
		// so a status poll can see that a login exists without ever raising
		// the macOS Keychain prompt; the identity comes from the state
		// database alone and is simply absent when only the keyring mirror
		// survives. That matches the reference, and it is the safe ordering:
		// fingerprinting would mean reading the secret.
		cred := p.antigravityStateCredential()
		if cred == nil && !antigravityKeyringExists() {
			return LiveAuthStatus{}, nil
		}
		identity := ""
		if cred != nil {
			identity = antigravityIdentity(cred.Token.RefreshToken)
		}
		return LiveAuthStatus{
			Present:  true,
			Identity: identity,
			Details:  "antigravity oauth",
		}, nil
	}
}

func (p *gemini) oauthEmail() string {
	// Antigravity writes the address in clear text; gemini-cli hides it in
	// the id_token. Probe the OAuth files first for either.
	for _, name := range geminiAllCredentialFiles() {
		raw, err := os.ReadFile(p.livePath(name))
		if err != nil {
			continue
		}
		if email := geminiEmailFromCredentials(raw); email != "" {
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
	// An Antigravity login lives in the IDE's state database, so the file loop
	// above never sees it. Snapshot it into the same portable name the proxy
	// already reads, otherwise the saved account would have no credential to
	// serve from even though DetectLive reported it as present.
	stateIdentity, stateNotes := "", ""
	if !p.hasOAuth() {
		cred := p.antigravityStateCredential()
		notes := "Antigravity login (IDE state)"
		if cred == nil {
			// The IDE state has nothing, so fall back to the OS credential
			// store. This is the one place that reads the secret: the user
			// asked to save this account, and the snapshot is useless without
			// the material. DetectLive must never do this.
			cred = antigravityKeyringCredential()
			notes = "Antigravity login (OS keyring)"
		}
		if cred != nil {
			out, err := json.Marshal(cred)
			if err == nil {
				dest := filepath.Join(destDir, antigravityCredentialFiles[0])
				if err := os.WriteFile(dest, out, 0o600); err == nil {
					found = true
					stateIdentity = antigravityIdentity(cred.Token.RefreshToken)
					stateNotes = notes
				}
			}
		}
	}
	if !found {
		return SnapshotInfo{}, anyerr.Newf(
			"No live Gemini CLI auth found under "+p.configDir()+". Log in first, then save.",
			"NO_LIVE_AUTH",
			anyerr.WithSuggestions("Run `gemini` and sign in, then try again."))
	}
	if stateIdentity != "" {
		info.Identity = stateIdentity
		info.Notes = stateNotes
	} else if p.hasOAuth() {
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

// GatewayUpstreamWithKey resolves the upstream for a Gemini gateway from a
// profile API key held in memory.
//
// A gateway has no account snapshot: the credential lives in the profile store,
// which owns it (invariant #6). It must therefore never be copied into a
// snapshot .env. ResolveUpstream runs once per request, so writing it to disk
// there is not just a redundant copy — it races any concurrent `anypick backup`
// with a half-written file, and a stale copy keeps resolving after the profile
// key is rotated or deleted (invariant #4). Passing the key through memory
// removes the write entirely.
func (p *gemini) GatewayUpstreamWithKey(apiKey string) (*proxy.Upstream, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, anyerr.Newf("No Gemini API key in this gateway profile.", "NO_CREDENTIAL")
	}
	return geminiKeyUpstream(apiKey)
}
