package provider

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

// workbuddy mirrors the OpenCode provider (provider/opencode.go): a file-backed
// account source under ~/.workbuddy (XDG-aware) whose accounts can be proxied
// to other clients. WorkBuddy is an OpenAI-compatible agent, so its managed
// proxy serves OpenAI-speaking borrowing clients (claude/codex/opencode).
//
// The proxy server itself (ResolveUpstream + server selection) is wired
// separately, the same way OpenCode's proxy lives in opencode-proxy-lifecycle.
type workbuddy struct{ home string }

func newWorkBuddy(home string) *workbuddy { return &workbuddy{home: home} }

func (p *workbuddy) ID() string          { return "workbuddy" }
func (p *workbuddy) Name() string        { return "WorkBuddy AI" }
func (p *workbuddy) Description() string { return "Manages ~/.workbuddy credentials" }
func (p *workbuddy) CanProxy() bool      { return true }

func (p *workbuddy) ShortName() string { return "WorkBuddy" }

func (p *workbuddy) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.WorkBuddyAccountAdapter(p.ID(), accountName)
}

func (p *workbuddy) dataDir() string {
	if env := os.Getenv("WORKBUDDY_DATA_DIR"); env != "" {
		return expandHome(env)
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" && expandHome(p.home) == homeDirOf() {
		return filepath.Join(expandHome(xdg), "workbuddy")
	}
	return filepath.Join(expandHome(p.home), ".workbuddy")
}

func (p *workbuddy) authPath() string {
	if env := os.Getenv("WORKBUDDY_AUTH_PATH"); env != "" {
		return expandHome(env)
	}
	return filepath.Join(p.dataDir(), "auth.json")
}

func (p *workbuddy) DetectLive() (LiveAuthStatus, error) {
	if _, err := os.Stat(p.authPath()); err != nil {
		if os.IsNotExist(err) {
			return LiveAuthStatus{}, nil
		}
		return LiveAuthStatus{}, err
	}
	return LiveAuthStatus{Present: true, Identity: "workbuddy"}, nil
}

func (p *workbuddy) Backup(destDir string) (SnapshotInfo, error) {
	src := p.authPath()
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, anyerr.Newf(
				"No live WorkBuddy auth found at "+src+". Sign in to WorkBuddy first, then save.",
				"NO_LIVE_AUTH",
				anyerr.WithSuggestions("Open WorkBuddy and sign in, then try again."))
		}
		return SnapshotInfo{}, err
	}
	if err := os.WriteFile(filepath.Join(destDir, "auth.json"), data, 0o600); err != nil {
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{CredentialKind: "native"}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) == nil {
		info.Identity = workBuddyIdentity(parsed)
	}
	return info, nil
}

func (p *workbuddy) Restore(srcDir string) error {
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

func (p *workbuddy) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	raw, err := os.ReadFile(filepath.Join(srcDir, "auth.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotInfo{}, nil
		}
		return SnapshotInfo{}, err
	}
	info := SnapshotInfo{CredentialKind: "native"}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) == nil {
		info.Identity = workBuddyIdentity(parsed)
	}
	return info, nil
}

// workBuddyIdentity extracts a display identity from the auth document without
// assuming its exact shape: prefer an email-looking field, else the first
// string value. Best-effort only — an empty result is acceptable.
func workBuddyIdentity(data map[string]json.RawMessage) string {
	for _, key := range []string{"email", "user", "username", "account", "identity"} {
		if raw, ok := data[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
	}
	for _, raw := range data {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}
