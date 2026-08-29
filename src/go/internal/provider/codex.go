package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/browserlogin"
	"github.com/outof0/anypick/go/internal/transport"
)

// codex mirrors CodexProvider: live auth is ~/.codex/auth.json. Detection,
// backup, restore, and snapshot matching all use that one authority.
type codex struct{ home string }

const codexLoginMethodID = "chatgpt-subscription"

func newCodex(home string) *codex { return &codex{home: home} }

func (p *codex) ID() string          { return "codex" }
func (p *codex) Name() string        { return "OpenAI Codex" }
func (p *codex) Description() string { return "Manages ~/.codex/auth.json" }
func (p *codex) CanProxy() bool      { return true }

func (p *codex) AccountAdapter(accountName string, proxyEnabled bool) transport.Adapter {
	return transport.CodexAccountAdapter(p.ID(), accountName, proxyEnabled)
}

// loginCommand mirrors CodexProvider.loginCommand (src/providers/codex.ts).
func (p *codex) loginCommand() browserlogin.Command {
	names := []string{"codex"}
	if env := os.Getenv("CODEX_BINARY"); env != "" {
		names = []string{env, "codex"}
	}
	return browserlogin.Command{
		Names: names,
		ExtraPaths: []string{
			filepath.Join(expandHome(p.home), ".local", "bin", "codex"),
			"/opt/homebrew/bin/codex",
			"/usr/local/bin/codex",
		},
		Args: []string{"login"},
	}
}

// LoginMethods mirrors CodexProvider.loginMethods.
func (p *codex) LoginMethods() []LoginMethod {
	return []LoginMethod{
		browserlogin.Availability(LoginMethod{
			ID:          codexLoginMethodID,
			Label:       "ChatGPT subscription",
			Description: "OpenAI Codex browser sign-in",
			Provenance:  "official",
			Preferred:   true,
			Source:      "codex",
		}, p.loginCommand()),
	}
}

// Login mirrors CodexProvider.login: the file credential store is asserted
// first so the OAuth flow lands in auth.json where snapshots can see it.
func (p *codex) Login(ctx context.Context, methodID, source string) error {
	if methodID != codexLoginMethodID || (source != "" && source != "codex") {
		return anyerr.Newf("Unknown Codex sign-in method.", "PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	if err := AssertFileCredentialStore(p.codexHome()); err != nil {
		return err
	}
	return browserlogin.Run(ctx, p.loginCommand(), expandHome(p.home))
}

func (p *codex) codexHome() string {
	if env := os.Getenv("CODEX_HOME"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(expandHome(p.home), ".codex")
}

func (p *codex) authPath() string {
	if env := os.Getenv("CODEX_AUTH_PATH"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(p.codexHome(), "auth.json")
}

// codexAuthJSON mirrors hasCodexAuth: API key or OAuth token material.
func codexAuthJSON(data map[string]json.RawMessage) bool {
	if raw, ok := data["OPENAI_API_KEY"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return true
		}
	}
	raw, ok := data["tokens"]
	if !ok {
		return false
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(raw, &tokens) != nil {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		if _, ok := tokens[key]; ok {
			return true
		}
	}
	return false
}

func (p *codex) DetectLive() (LiveAuthStatus, error) {
	// A non-file credential store means auth.json is not the authoritative
	// login; report absence with remediation instead of a false positive.
	store := InspectCodexCredentialStore(p.codexHome())
	if !store.SupportsSnapshotSwitch {
		return LiveAuthStatus{
			Details: "Credential store: " + string(store.Mode) + ". " + CodexCredentialStoreGuidance(store),
		}, nil
	}
	path := p.authPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LiveAuthStatus{}, nil
		}
		return LiveAuthStatus{}, err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return LiveAuthStatus{Present: true, Details: "auth.json present (unreadable)"}, nil
	}
	if !codexAuthJSON(data) {
		return LiveAuthStatus{}, nil
	}
	info := codexSnapshotInfo(data)
	status := LiveAuthStatus{Present: true, Identity: info.Identity}
	if v, ok := data["auth_mode"]; ok {
		var mode string
		if json.Unmarshal(v, &mode) == nil && mode != "" {
			status.Details = mode
		}
	}
	if tokensRaw, ok := data["tokens"]; ok {
		var tokens map[string]json.RawMessage
		if json.Unmarshal(tokensRaw, &tokens) == nil {
			if idRaw, ok := tokens["account_id"]; ok {
				var id string
				if json.Unmarshal(idRaw, &id) == nil && id != "" {
					status.AccountID = id
					suffix := id
					if len(suffix) > 8 {
						suffix = suffix[:8]
					}
					if status.Details != "" {
						status.Details += " "
					}
					status.Details += "acct:" + suffix
				}
			}
		}
	}
	return status, nil
}

func (p *codex) Backup(destDir string) (SnapshotInfo, error) {
	// auth.json is only the authoritative login when the credential store is
	// file-backed; snapshotting under keyring/auto would capture a stale file.
	if err := AssertFileCredentialStore(p.codexHome()); err != nil {
		return SnapshotInfo{}, err
	}
	src := p.authPath()
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, anyerr.Newf(
				"No live Codex auth.json found at "+src+". Log in first, then save.",
				"NO_LIVE_AUTH",
				anyerr.WithSuggestions("Sign in with the official tool, then try again."),
			)
		}
		return SnapshotInfo{}, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return SnapshotInfo{}, err
	}
	dest := filepath.Join(destDir, "auth.json")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) == nil {
		info = codexSnapshotInfo(parsed)
	}
	return info, nil
}

// Restore refuses to write auth.json unless it is the authoritative Codex
// credential store: with keyring/auto storage a successful-looking write would
// leave the real CLI login untouched while AnyPick reports success.
func (p *codex) Restore(srcDir string) error {
	if err := AssertFileCredentialStore(p.codexHome()); err != nil {
		return err
	}
	src := filepath.Join(srcDir, "auth.json")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot is missing auth.json: %s", src)
		}
		return err
	}
	dest := p.authPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

// PreflightRestore mirrors CodexProvider.preflightRestore: refuse before any
// checkpoint is taken so an unsafe owner/store fails without rollback work.
func (p *codex) PreflightRestore(_ string) error {
	return AssertFileCredentialStore(p.codexHome())
}

func (p *codex) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	data, err := os.ReadFile(filepath.Join(srcDir, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, nil
		}
		return SnapshotInfo{}, err
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) != nil {
		return SnapshotInfo{}, nil
	}
	return codexSnapshotInfo(parsed), nil
}

// codexSnapshotInfo extracts a best-effort display identity the same way
// extractCodexIdentity does: email/name from JWT claims, then chatgpt:<id>,
// then "API key".
func codexSnapshotInfo(data map[string]json.RawMessage) SnapshotInfo {
	tokensRaw, ok := data["tokens"]
	if !ok {
		if keyRaw, ok := data["OPENAI_API_KEY"]; ok {
			var key string
			if json.Unmarshal(keyRaw, &key) == nil && strings.TrimSpace(key) != "" {
				return SnapshotInfo{Identity: "API key"}
			}
		}
		return SnapshotInfo{}
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(tokensRaw, &tokens) != nil {
		return SnapshotInfo{}
	}
	fromID := jwtClaims(stringField(tokens, "id_token"))
	fromAccess := jwtClaims(stringField(tokens, "access_token"))

	email := firstNonEmpty(
		stringClaim(fromID, "email"),
		stringClaim(fromAccess, "email"),
		nestedEmail(fromAccess, "https://api.openai.com/profile"),
		nestedEmail(fromID, "https://api.openai.com/profile"),
	)
	if email != "" {
		return SnapshotInfo{Identity: email}
	}
	if name := firstNonEmpty(stringClaim(fromID, "name"), stringClaim(fromAccess, "name")); name != "" {
		return SnapshotInfo{Identity: name}
	}
	if acct := stringField(tokens, "account_id"); acct != "" {
		suffix := acct
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		return SnapshotInfo{Identity: "chatgpt:" + suffix}
	}
	return SnapshotInfo{}
}

func stringField(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func stringClaim(claims map[string]interface{}, key string) string {
	if claims == nil {
		return ""
	}
	if v, ok := claims[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func nestedEmail(claims map[string]interface{}, profileKey string) string {
	if claims == nil {
		return ""
	}
	profile, ok := claims[profileKey].(map[string]interface{})
	if !ok {
		return ""
	}
	return stringClaim(profile, "email")
}

func jwtClaims(token string) map[string]interface{} {
	if token == "" {
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 || parts[1] == "" {
		return nil
	}
	b64 := strings.NewReplacer("-", "+", "_", "/").Replace(parts[1])
	if pad := len(b64) % 4; pad != 0 {
		b64 += strings.Repeat("=", 4-pad)
	}
	payload, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	var claims map[string]interface{}
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func (codex *codex) ShortName() string { return "Codex" }
