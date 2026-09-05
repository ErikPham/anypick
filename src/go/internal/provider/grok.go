package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/browserlogin"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/transport"
)

// grok mirrors GrokProvider: ~/.grok/auth.json holds OIDC sessions keyed by
// account; no external binary needed.
type grok struct{ home string }

const grokLoginMethodID = "grok-subscription"

func newGrok(home string) *grok { return &grok{home: home} }

func (p *grok) ID() string { return "grok" }
func (p *grok) Name() string {
	return "xAI Grok"
}
func (p *grok) Description() string {
	return "Manages ~/.grok/auth.json + OpenAI/Anthropic compat proxy (Codex + Claude)"
}
func (p *grok) CanProxy() bool { return true }

func (p *grok) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.GrokAccountAdapter(p.ID(), accountName)
}

// loginCommand mirrors GrokProvider.loginCommand (src/providers/grok.ts).
func (p *grok) loginCommand() browserlogin.Command {
	names := []string{"grok"}
	if env := os.Getenv("GROK_BINARY"); env != "" {
		names = []string{env, "grok"}
	}
	return browserlogin.Command{
		Names:      names,
		ExtraPaths: []string{filepath.Join(expandHome(p.home), ".grok", "bin", "grok")},
		Args:       []string{"login", "--oauth"},
	}
}

// LoginMethods mirrors GrokProvider.loginMethods.
func (p *grok) LoginMethods() []LoginMethod {
	return []LoginMethod{
		browserlogin.Availability(LoginMethod{
			ID:          grokLoginMethodID,
			Label:       "Grok subscription",
			Description: "Grok browser sign-in through auth.x.ai",
			Provenance:  "official",
			Preferred:   true,
		}, p.loginCommand()),
	}
}

// Login mirrors GrokProvider.login (no source-scoped methods).
func (p *grok) Login(ctx context.Context, methodID, source string) error {
	if methodID != grokLoginMethodID || source != "" {
		return anyerr.Newf("Unknown Grok sign-in method.", "PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	return browserlogin.Run(ctx, p.loginCommand(), expandHome(p.home))
}

func (p *grok) authPath() string {
	if env := os.Getenv("GROK_AUTH_PATH"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(expandHome(p.home), ".grok", "auth.json")
}

// grokIdentity finds the first email-looking value across session objects
// (firstEmail in TS).
func grokIdentity(data map[string]json.RawMessage) string {
	for _, raw := range data {
		var session map[string]json.RawMessage
		if json.Unmarshal(raw, &session) != nil {
			continue
		}
		for _, key := range []string{"email", "user_email"} {
			if v := stringField(session, key); strings.Contains(v, "@") {
				return v
			}
		}
		// Nested user object variant.
		if userRaw, ok := session["user"]; ok {
			var user map[string]json.RawMessage
			if json.Unmarshal(userRaw, &user) == nil {
				if v := stringField(user, "email"); strings.Contains(v, "@") {
					return v
				}
			}
		}
	}
	return ""
}

func (p *grok) DetectLive() (LiveAuthStatus, error) {
	raw, err := os.ReadFile(p.authPath())
	if err != nil {
		if os.IsNotExist(err) {
			return LiveAuthStatus{}, nil
		}
		return LiveAuthStatus{}, err
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return LiveAuthStatus{Present: true, Details: "auth.json present (unreadable)"}, nil
	}
	sessions := 0
	for _, v := range data {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) == nil && len(obj) > 0 {
			sessions++
		}
	}
	if sessions == 0 {
		return LiveAuthStatus{}, nil
	}
	return LiveAuthStatus{Present: true, Identity: grokIdentity(data)}, nil
}

func (p *grok) Backup(destDir string) (SnapshotInfo, error) {
	src := p.authPath()
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, anyerr.Newf(
				"No live Grok auth.json found at "+src+". Log in first, then save.",
				"NO_LIVE_AUTH",
				anyerr.WithSuggestions("Sign in with the official tool, then try again."))
		}
		return SnapshotInfo{}, err
	}
	if err := os.WriteFile(filepath.Join(destDir, "auth.json"), data, 0o600); err != nil {
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) == nil {
		info.Identity = grokIdentity(parsed)
	}
	return info, nil
}

func (p *grok) Restore(srcDir string) error {
	data, err := os.ReadFile(filepath.Join(srcDir, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return errSnapshotMissing("auth.json")
		}
		return err
	}
	dest := p.authPath()
	// dest is the user's real ~/.grok/auth.json. A truncated write here
	// destroys their live login with nothing to fall back on, so replace by
	// rename rather than overwriting in place.
	return atomicfile.Write(dest, data, 0o600)
}

func (p *grok) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	raw, err := os.ReadFile(filepath.Join(srcDir, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, nil
		}
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) == nil {
		info.Identity = grokIdentity(parsed)
	}
	return info, nil
}

func (grok *grok) ShortName() string { return "Grok" }

// ResolveUpstream points the engine's shared proxy at cli-chat-proxy.grok.com
// using this account's saved OIDC session. The token is short-lived, so it is
// supplied per request by grokEnsureAccessToken (OIDC refresh) rather than
// frozen at start; x-grok-* + user-agent headers are injected on every
// forwarded request. The same base serves both OpenAI and native Anthropic.
func (p *grok) ResolveUpstream(snapshotDir string) (*proxy.Upstream, error) {
	return grokUpstream(filepath.Join(snapshotDir, "auth.json"))
}

// GatewayUpstream resolves the upstream for a Grok gateway from the live x.ai
// OIDC session (~/.grok/auth.json) shared with the account flow. A gateway
// carries no account snapshot and no API key; it reuses the session the user
// established with `grok login`, refreshing the token per request.
func (p *grok) GatewayUpstream() (*proxy.Upstream, error) {
	return grokUpstream(p.authPath())
}
