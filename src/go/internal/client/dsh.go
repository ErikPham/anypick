package client

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/transport"
)

// dshAdapter ports the DeepSeek Harness (dsh) CLI. dsh is a Cordis-based
// harness whose DeepSeek adapter (route deepseek-official) reads its API key
// from an environment variable (DEEPSEEK_API_KEY) and its endpoint from
// $DEEPSEEK_BASE_URL. The key lives in the harness home's .env (read by dsh
// as environment layers); the base URL must come from the launching
// environment instead — newer dsh refuses to start when .env sets it, since
// it decides how the process reaches the network. Because baseURL is
// overridable, dsh can ride AnyPick's gateway to any upstream, not only
// DeepSeek.
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

	// AnyPick-owned block markers inside the user's settings.yaml. Unlike
	// cordis patch rows (whole-config replace), a user-settings section
	// overrides per field and hot-reloads, so a partial llm-deepseek section
	// cannot wipe the plugin's other settings.
	dshSettingsBegin = "# anypick:dsh-settings"
	dshSettingsEnd   = "# anypick:dsh-settings:end"
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

// dshSettingsPath is the user settings document dsh hot-reloads
// (~/.dsh/settings.yaml in default setups). A JSON document, if the user
// chose one, is left alone: only the default YAML mount is managed.
func dshSettingsPath(home string) string {
	return filepath.Join(dshHome(home), "settings.yaml")
}

func (a *dshAdapter) Apply(ctx *ApplyContext) (*ApplyResult, error) {
	targetHome := ctx.Home
	if ctx.IsolatedHome != "" {
		targetHome = ctx.IsolatedHome
	}
	baseURL := strings.TrimSuffix(ctx.Endpoint, "/") + "/v1"

	// The secret may live in .env (a credential, not launch config). The base
	// URL must not: newer dsh aborts when a layered file sets it.
	dotEnv := map[string]string{}
	if ctx.APIKey != "" {
		dotEnv[dshEnvKeyAPI] = ctx.APIKey
	}
	launcherEnv := map[string]string{}
	for k, v := range dotEnv {
		launcherEnv[k] = v
	}
	if ctx.Endpoint != "" {
		launcherEnv[dshEnvKeyBase] = baseURL
	}
	model := firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel)
	managedEnvKeys := sortedKeys(launcherEnv)

	if ctx.DryRun {
		return &ApplyResult{
			ManagedPaths: []string{
				dshEnvPath(targetHome),
				dshPatchPath(targetHome),
				dshSettingsPath(targetHome),
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
	if err := upsertEnvFile(dshEnvPath(targetHome), dotEnv); err != nil {
		return nil, err
	}
	// upsertEnvFile only merges: strip any base URL older AnyPick versions
	// wrote, or every dsh invocation keeps aborting on the stale line.
	if err := stripKeysFromEnv(dshEnvPath(targetHome), []string{dshEnvKeyBase}); err != nil {
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

	// User settings: pin the adapter endpoint so plain `dsh` runs ride the
	// managed proxy with no exports. The llm-deepseek section overrides per
	// field and hot-reloads — no restart, no launch environment needed.
	if ctx.Endpoint != "" {
		if err := mergeDshSettingsEndpoint(dshSettingsPath(targetHome), baseURL); err != nil {
			return nil, err
		}
		managedPaths = append(managedPaths, dshSettingsPath(targetHome))
	}

	// Persistent (non-isolated) also emits launcher env files the desktop app
	// sources before spawning dsh. The base URL travels here — the launching
	// environment dsh requires — never in .env.
	if ctx.IsolatedHome == "" {
		managedPaths = append(managedPaths, writeClientEnvFiles(ctx.AnyPickRoot, "dsh", launcherEnv)...)
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
		return writeDshFile(path, []byte(block))
	}
	if err != nil {
		return err
	}
	if !dshPatchIsSequence(string(raw)) {
		return anyerr.Newf(
			"Refusing to modify non-sequence cordis patch at "+path+".",
			"CLIENT_CONFIG_INVALID")
	}
	cleaned, err := stripMarkedBlock(string(raw), dshPatchBegin, dshPatchEnd, path)
	if err != nil {
		return err
	}
	out := strings.TrimRight(cleaned, "\n")
	if out != "" {
		out += "\n"
	}
	out += block
	return writeDshFile(path, []byte(out))
}

// writeDshFile replaces a user-owned config file atomically (rename, never
// truncation): a crash mid-write must not leave a half-written login or
// settings file. An existing file keeps its permission bits; only brand-new
// files are created 0600.
func writeDshFile(path string, data []byte) error {
	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicfile.Write(path, data, perm)
}

// stripMarkedBlock removes the lines between begin and end markers,
// preserving everything else. Markers must be balanced: a begin without its
// end (or an end without a begin) fails closed instead of silently dropping
// the tail of the user's file on the next overwrite.
func stripMarkedBlock(s, begin, end, what string) (string, error) {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case begin:
			depth++
			continue
		case end:
			if depth == 0 {
				return "", anyerr.Newf(
					"Unbalanced AnyPick block in "+what+": end marker without begin.",
					"CLIENT_CONFIG_INVALID")
			}
			depth--
			continue
		}
		if depth == 0 {
			out = append(out, line)
		}
	}
	if depth != 0 {
		return "", anyerr.Newf(
			"Unbalanced AnyPick block in "+what+": begin marker without end; refusing to rewrite.",
			"CLIENT_CONFIG_INVALID")
	}
	return strings.Join(out, "\n"), nil
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

// mergeDshSettingsEndpoint writes or updates the AnyPick-managed
// llm-deepseek section in the user's settings.yaml. The settings service
// merges per field and hot-reloads, so unrelated user content (comments,
// other sections) survives untouched. A user-owned top-level llm-deepseek
// section outside the markers is refused instead of duplicated: two mapping
// keys would make dsh fail its boot loud.
func mergeDshSettingsEndpoint(path, baseURL string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := dshSettingsWithEndpoint(string(raw), baseURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeDshFile(path, []byte(out))
}

func dshSettingsBlock(baseURL string) string {
	return dshSettingsBegin + "\nllm-deepseek:\n  baseURL: " + baseURL + "\n" + dshSettingsEnd + "\n"
}

func dshSettingsKeyLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, quote := range []string{"", `"`, `'`} {
		prefix := quote + "llm-deepseek" + quote + ":"
		if strings.HasPrefix(trimmed, prefix) {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			if rest == "" || strings.HasPrefix(rest, "#") {
				return true
			}
		}
	}
	return false
}

func dshSettingsWithEndpoint(existing, baseURL string) (string, error) {
	stripped, err := stripMarkedBlock(existing, dshSettingsBegin, dshSettingsEnd, "settings.yaml")
	if err != nil {
		return "", err
	}
	lines := strings.Split(stripped, "\n")
	var out []string
	for _, line := range lines {
		if dshSettingsKeyLine(line) {
			return "", anyerr.Newf(
				"Your settings.yaml already has its own llm-deepseek: section; "+
					"remove it or point it at the proxy before AnyPick manages the endpoint.",
				"CLIENT_CONFIG_INVALID")
		}
		out = append(out, line)
	}
	cleaned := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if cleaned != "" {
		cleaned += "\n"
	}
	return cleaned + dshSettingsBlock(baseURL), nil
}

// stripDshSettingsBlock removes the AnyPick-managed settings section,
// preserving everything else. Empty remainder means the file is ours alone.
// Unbalanced markers fail closed: Reset must not rewrite a file it cannot
// parse.
func stripDshSettingsBlock(s string) (string, bool, error) {
	stripped, err := stripMarkedBlock(s, dshSettingsBegin, dshSettingsEnd, "settings.yaml")
	if err != nil {
		return "", false, err
	}
	rest := strings.TrimSpace(stripped)
	return rest, rest == "", nil
}

// stripDshManagedBlock removes the AnyPick-managed block (between markers),
// preserving every other line the user or another tool wrote. Unbalanced
// markers fail closed (see stripMarkedBlock).
func stripDshManagedBlock(s string) (string, error) {
	return stripMarkedBlock(s, dshPatchBegin, dshPatchEnd, "cordis.patch.yml")
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
		cleaned, cerr := stripDshManagedBlock(string(raw))
		if cerr != nil {
			return cerr
		}
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			_ = os.Remove(patchPath)
		} else if werr := writeDshFile(patchPath, []byte(cleaned+"\n")); werr != nil {
			return werr
		}
	}
	settingsPath := dshSettingsPath(a.home)
	if raw, err := os.ReadFile(settingsPath); err == nil {
		rest, empty, rerr := stripDshSettingsBlock(string(raw))
		if rerr != nil {
			return rerr
		}
		if empty {
			_ = os.Remove(settingsPath)
		} else if werr := writeDshFile(settingsPath, []byte(rest+"\n")); werr != nil {
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
	if raw, err := os.ReadFile(dshSettingsPath(a.home)); err == nil {
		if strings.Contains(string(raw), dshSettingsBegin) {
			res.Present = true
		}
	}
	if res.Present {
		res.Summary = "AnyPick proxy configured"
	}
	return res, nil
}
