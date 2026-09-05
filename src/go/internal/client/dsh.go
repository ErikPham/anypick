package client

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

// dshAdapter ports the DeepSeek Harness (dsh) CLI. dsh is a Cordis-based
// harness whose DeepSeek adapter (route deepseek-official) reads its API key
// from an environment variable (DEEPSEEK_API_KEY) and its endpoint from
// $DEEPSEEK_BASE_URL. AnyPick points both at the managed proxy by writing the
// harness home's .env (read by dsh as environment layers) and a home-level
// cordis patch that pins the default model. Because baseURL is overridable, dsh
// can ride AnyPick's gateway to any upstream, not only DeepSeek.
type dshAdapter struct{ home string }

func newDsh(home string) *dshAdapter { return &dshAdapter{home: home} }

const (
	dshEnvKeyAPI  = "DEEPSEEK_API_KEY"
	dshEnvKeyBase = "DEEPSEEK_BASE_URL"

	// AnyPick-owned block markers inside the user's cordis.patch.yml. dsh
	// applies every row in that file after the profile, so appending a row
	// targeted by id replaces/applies it (Cordis patch semantics).
	dshPatchBegin = "# anypick:dsh"
	dshPatchEnd   = "# anypick:dsh:end"

	// dsh's default-model plugin id and the route its DeepSeek adapter owns.
	dshDefaultModelPlugin = "@deepseek-ai/dsh-agent-default-model"
	dshProviderRoute      = "deepseek-official"
)

func (a *dshAdapter) ID() string   { return "dsh" }
func (a *dshAdapter) Name() string { return "DeepSeek Harness" }
func (a *dshAdapter) ShortName() string {
	return "dsh"
}

func (a *dshAdapter) Capabilities() *Capabilities {
	return &Capabilities{
		ID:                         "dsh",
		AcceptedProtocols:          []transport.Protocol{transport.OpenAI},
		SupportsIsolatedHome:       true,
		SupportsPersistentConfig:   true,
		SupportsEnvironmentOverlay: true,
		ProtocolPreference:         transport.OpenAI,
	}
}

// dshHome resolves the harness home: $DSH_HOME when set and we are targeting
// the live home, otherwise ~/.dsh. Mirrors gemini's config-dir resolution so an
// isolated run never reaches the user's real harness home.
func dshHome(home string) string {
	if env := os.Getenv("DSH_HOME"); env != "" && home == liveHome() {
		return env
	}
	return filepath.Join(home, ".dsh")
}

func dshEnvPath(home string) string   { return filepath.Join(dshHome(home), ".env") }
func dshPatchPath(home string) string { return filepath.Join(dshHome(home), "cordis.patch.yml") }

func (a *dshAdapter) Apply(ctx *ApplyContext) (*ApplyResult, error) {
	targetHome := ctx.Home
	if ctx.IsolatedHome != "" {
		targetHome = ctx.IsolatedHome
	}
	baseURL := strings.TrimSuffix(ctx.Endpoint, "/") + "/v1"

	env := map[string]string{}
	if ctx.APIKey != "" {
		env[dshEnvKeyAPI] = ctx.APIKey
	}
	if ctx.Endpoint != "" {
		env[dshEnvKeyBase] = baseURL
	}
	model := firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel)
	managedEnvKeys := sortedKeys(env)

	if ctx.DryRun {
		return &ApplyResult{
			ManagedPaths: []string{
				dshEnvPath(targetHome),
				dshPatchPath(targetHome),
				filepath.Join(ctx.AnyPickRoot, "clients", "dsh", "env.sh"),
				filepath.Join(ctx.AnyPickRoot, "clients", "dsh", "env.ps1"),
			},
			ManagedEnvKeys: managedEnvKeys,
		}, nil
	}

	// .env: dsh loads $DSH_HOME/.env as environment layers.
	if err := os.MkdirAll(dshHome(targetHome), 0o700); err != nil {
		return nil, err
	}
	if err := upsertEnvFile(dshEnvPath(targetHome), env); err != nil {
		return nil, err
	}
	managedPaths := []string{dshEnvPath(targetHome)}

	// cordis patch: pin the default model so the harness routes the chosen
	// account's model instead of its own bundled default.
	if model != "" {
		if err := mergeDshCordisPatch(dshPatchPath(targetHome), model); err != nil {
			return nil, err
		}
		managedPaths = append(managedPaths, dshPatchPath(targetHome))
	}

	// Persistent (non-isolated) also emits launcher env files the desktop app
	// sources before spawning dsh.
	if ctx.IsolatedHome == "" {
		managedPaths = append(managedPaths, writeClientEnvFiles(ctx.AnyPickRoot, "dsh", env)...)
	}

	return &ApplyResult{ManagedPaths: managedPaths, ManagedEnvKeys: managedEnvKeys}, nil
}

func dshCordisPatchBlock(model string) string {
	var b strings.Builder
	b.WriteString(dshPatchBegin)
	b.WriteString("\n- name: '")
	b.WriteString(dshDefaultModelPlugin)
	b.WriteString("'\n")
	b.WriteString("  config:\n")
	b.WriteString("    provider: ")
	b.WriteString(dshProviderRoute)
	b.WriteString("\n")
	b.WriteString("    model: ")
	b.WriteString(model)
	b.WriteString("\n")
	b.WriteString(dshPatchEnd)
	b.WriteString("\n")
	return b.String()
}

// mergeDshCordisPatch writes or updates the AnyPick-managed block in the
// home-level cordis patch. cordis.patch.yml is a sequence of rows; we append a
// row targeted by id, which Cordis applies over the profile. A non-sequence
// file (map, etc.) is left untouched and rejected, exactly as the other
// adapters refuse to overwrite unreadable config.
func mergeDshCordisPatch(path, model string) error {
	block := dshCordisPatchBlock(model)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return os.WriteFile(path, []byte(block), 0o600)
	}
	if err != nil {
		return err
	}
	if !dshPatchIsSequence(string(raw)) {
		return anyerr.Newf(
			"Refusing to modify non-sequence cordis patch at "+path+".",
			"CLIENT_CONFIG_INVALID")
	}
	cleaned := stripDshManagedBlock(string(raw))
	out := strings.TrimRight(cleaned, "\n")
	if out != "" {
		out += "\n"
	}
	out += block
	return os.WriteFile(path, []byte(out), 0o600)
}

func dshPatchIsSequence(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return strings.HasPrefix(t, "-")
	}
	return true
}

// stripDshManagedBlock removes the AnyPick-managed block (between markers),
// preserving every other line the user or another tool wrote.
func stripDshManagedBlock(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inside := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == dshPatchBegin {
			inside = true
			continue
		}
		if trimmed == dshPatchEnd {
			inside = false
			continue
		}
		if inside {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (a *dshAdapter) Reset() error {
	liveEnv := dshEnvPath(a.home)
	if _, err := os.Stat(liveEnv); err == nil {
		if err := stripKeysFromEnv(liveEnv, []string{dshEnvKeyAPI, dshEnvKeyBase}); err != nil {
			return err
		}
	}
	patchPath := dshPatchPath(a.home)
	if _, err := os.Stat(patchPath); err == nil {
		raw, rerr := os.ReadFile(patchPath)
		if rerr != nil {
			return rerr
		}
		cleaned := strings.TrimSpace(stripDshManagedBlock(string(raw)))
		if cleaned == "" {
			_ = os.Remove(patchPath)
		} else if werr := os.WriteFile(patchPath, []byte(cleaned+"\n"), 0o600); werr != nil {
			return werr
		}
	}
	removeClientEnvFiles(anyPickRootFallback(), "dsh")
	return nil
}

func (a *dshAdapter) Inspect() (*InspectResult, error) {
	res := &InspectResult{Present: false, Summary: "no dsh config"}
	if raw, err := os.ReadFile(dshEnvPath(a.home)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, dshEnvKeyAPI+"=") || strings.HasPrefix(t, dshEnvKeyBase+"=") {
				res.Present = true
			}
		}
	}
	if raw, err := os.ReadFile(dshPatchPath(a.home)); err == nil {
		if strings.Contains(string(raw), dshPatchBegin) {
			res.Present = true
		}
	}
	if res.Present {
		res.Summary = "AnyPick proxy configured"
	}
	return res, nil
}
