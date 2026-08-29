package provider

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

// opencode mirrors OpenCodeProvider: ~/.local/share/opencode/auth.json
// (XDG-aware) plus account.json.
type opencode struct{ home string }

func newOpenCode(home string) *opencode { return &opencode{home: home} }

func (p *opencode) ID() string          { return "opencode" }
func (p *opencode) Name() string        { return "OpenCode" }
func (p *opencode) Description() string { return "Manages ~/.local/share/opencode/auth.json" }
func (p *opencode) CanProxy() bool      { return true }

func (p *opencode) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.OpenCodeAccountAdapter(p.ID(), accountName)
}

func (p *opencode) dataDir() string {
	if env := os.Getenv("OPENCODE_DATA_DIR"); env != "" {
		return expandHome(env)
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" && expandHome(p.home) == homeDirOf() {
		return filepath.Join(xdg, "opencode")
	}
	return filepath.Join(expandHome(p.home), ".local", "share", "opencode")
}

func (p *opencode) authPath() string {
	if env := os.Getenv("OPENCODE_AUTH_PATH"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(p.dataDir(), "auth.json")
}

func homeDirOf() string {
	home, _ := os.UserHomeDir()
	return home
}

// openCodeIdentity mirrors extractOpenCodeIdentity: prefer an oauth entry's
// email (JWT claim or metadata), else provider:accountId prefix.
func openCodeIdentity(data map[string]json.RawMessage) string {
	for provider, raw := range data {
		var entry struct {
			Type      string `json:"type"`
			Access    string `json:"access"`
			AccountID string `json:"accountId"`
			Metadata  struct {
				Email string `json:"email"`
			} `json:"metadata"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Type != "oauth" {
			continue
		}
		if email := jwtEmail(entry.Access); email != "" {
			return email
		}
		if entry.Metadata.Email != "" {
			return entry.Metadata.Email
		}
		if entry.AccountID != "" {
			prefix := entry.AccountID
			if len(prefix) > 12 {
				prefix = prefix[:12]
			}
			return provider + ":" + prefix
		}
	}
	return ""
}

func jwtEmail(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Email == "" {
		return ""
	}
	return claims.Email
}

func (p *opencode) DetectLive() (LiveAuthStatus, error) {
	raw, err := os.ReadFile(p.authPath())
	if err != nil {
		if os.IsNotExist(err) {
			if status, ok := liveOpenCodeAuthStatus(); ok {
				return status, nil
			}
			return LiveAuthStatus{}, nil
		}
		return LiveAuthStatus{}, err
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || len(data) == 0 {
		// A live Zen/Go login can exist only in the CLI's sqlite store.
		if status, ok := liveOpenCodeAuthStatus(); ok {
			return status, nil
		}
		return LiveAuthStatus{}, nil
	}
	return LiveAuthStatus{Present: true, Identity: openCodeIdentity(data)}, nil
}

func (p *opencode) Backup(destDir string) (SnapshotInfo, error) {
	src := p.authPath()
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, anyerr.Newf(
				"No live OpenCode auth.json found at "+src+". Log in first, then save.",
				"NO_LIVE_AUTH",
				anyerr.WithSuggestions("Run `opencode auth login`, then try again."))
		}
		return SnapshotInfo{}, err
	}
	if err := os.WriteFile(filepath.Join(destDir, "auth.json"), data, 0o600); err != nil {
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) == nil {
		info.Identity = openCodeIdentity(parsed)
	}
	return info, nil
}

func (p *opencode) Restore(srcDir string) error {
	data, err := os.ReadFile(filepath.Join(srcDir, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return errSnapshotMissing("auth.json")
		}
		return err
	}
	dest := p.authPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

func (p *opencode) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
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
		info.Identity = openCodeIdentity(parsed)
	}
	return info, nil
}

func (opencode *opencode) ShortName() string { return "OpenCode" }
