package client

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/transport"
)

// codexAdapter ports the essential surface of createCodexClient:
// a managed block in ~/.codex/config.toml. The full TS adapter also drives the
// Desktop catalog and sticky live-mode state (src/core/codex-live-config.ts);
// those stay with the host composition until ported.
type codexAdapter struct{ home string }

func newCodex(home string) *codexAdapter { return &codexAdapter{home: home} }

const (
	codexBeginMarker = "# BEGIN anypick-managed"
	codexEndMarker   = "# END anypick-managed"
)

func (a *codexAdapter) ID() string   { return "codex" }
func (a *codexAdapter) Name() string { return "OpenAI Codex" }

func (a *codexAdapter) Capabilities() *Capabilities {
	return &Capabilities{
		ID:                       "codex",
		AcceptedProtocols:        []transport.Protocol{transport.OpenAI},
		SupportsIsolatedHome:     true,
		SupportsPersistentConfig: true,
		ProtocolPreference:       transport.OpenAI,
	}
}

func codexConfigPath(home string) string {
	if env := os.Getenv("CODEX_HOME"); env != "" {
		return filepath.Join(env, "config.toml")
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func (a *codexAdapter) Apply(ctx *ApplyContext) (*ApplyResult, error) {
	targetHome := ctx.Home
	if ctx.IsolatedHome != "" {
		targetHome = ctx.IsolatedHome
	}
	configPath := codexConfigPath(targetHome)

	model := firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel)
	base := strings.TrimSuffix(ctx.Endpoint, "/")

	var b strings.Builder
	b.WriteString(codexBeginMarker + "\n")
	if model != "" {
		b.WriteString("model = " + quoteTOML(model) + "\n")
	}
	b.WriteString("model_provider = \"anypick\"\n\n")
	b.WriteString("[model_providers.anypick]\n")
	b.WriteString("name = \"AnyPick\"\n")
	b.WriteString("base_url = " + quoteTOML(base+"/v1") + "\n")
	b.WriteString("env_key = \"ANYPICK_PROXY_API_KEY\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString(codexEndMarker + "\n")
	block := b.String()

	managedPaths := []string{configPath}
	if ctx.DryRun {
		return &ApplyResult{ManagedPaths: managedPaths, ManagedEnvKeys: []string{"ANYPICK_PROXY_API_KEY"}}, nil
	}

	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	stripped, hadBlock := stripManagedBlock(string(raw))
	next := reinsertManagedBlock(stripped, block, hadBlock)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, []byte(next), 0o600); err != nil {
		return nil, err
	}

	if ctx.AnyPickRoot != "" && ctx.IsolatedHome == "" {
		writeClientEnvFiles(ctx.AnyPickRoot, "codex", map[string]string{
			"ANYPICK_PROXY_API_KEY": ctx.APIKey,
		})
	}

	return &ApplyResult{
		ManagedPaths:   managedPaths,
		ManagedEnvKeys: []string{"ANYPICK_PROXY_API_KEY"},
	}, nil
}

// RestoreNative removes the managed block so Codex talks to its native login —
// the persistent direct-account path calls this instead of Apply.
func (a *codexAdapter) RestoreNative() error {
	path := codexConfigPath(a.home)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	stripped, changed := stripManagedBlock(string(raw))
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(stripped), 0o600)
}

func (a *codexAdapter) Reset() error { return a.RestoreNative() }

func (a *codexAdapter) Inspect() (*InspectResult, error) {
	raw, err := os.ReadFile(codexConfigPath(a.home))
	if err != nil {
		if os.IsNotExist(err) {
			return &InspectResult{Present: false, Issues: []string{"config.toml not found"}}, nil
		}
		return nil, err
	}
	if strings.Contains(string(raw), codexBeginMarker) {
		return &InspectResult{Present: true}, nil
	}
	return &InspectResult{Present: false, Issues: []string{"no anypick-managed block"}}, nil
}

// stripManagedBlock removes an existing managed block, reporting whether one
// was present.
func stripManagedBlock(content string) (string, bool) {
	begin := strings.Index(content, codexBeginMarker)
	end := strings.Index(content, codexEndMarker)
	if begin < 0 || end < 0 || end < begin {
		return content, false
	}
	before := strings.TrimRight(content[:begin], "\n")
	after := content[end+len(codexEndMarker):]
	after = strings.TrimLeft(after, "\n")
	switch {
	case before == "":
		return after, true
	case after == "":
		return before + "\n", true
	default:
		return before + "\n\n" + after, true
	}
}

// reinsertManagedBlock appends (or replaces) the managed block while keeping
// user content ahead of it.
func reinsertManagedBlock(content, block string, _ bool) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return block
	}
	return content + "\n\n" + block
}

func quoteTOML(s string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) + "\""
}

func (a *codexAdapter) ShortName() string { return "Codex" }
