package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

// geminiAdapter ports src/clients/gemini.ts: API-key/env runtime apply into
// ~/.gemini/.env plus a managed marker in settings.json.
type geminiAdapter struct{ home string }

func newGemini(home string) *geminiAdapter { return &geminiAdapter{home: home} }

func (a *geminiAdapter) ID() string   { return "gemini" }
func (a *geminiAdapter) Name() string { return "Gemini CLI" }
func (a *geminiAdapter) ShortName() string {
	return "Gemini"
}

func (a *geminiAdapter) Capabilities() *Capabilities {
	return &Capabilities{
		ID:                       "gemini",
		AcceptedProtocols:        []transport.Protocol{transport.OpenAI},
		SupportsIsolatedHome:     true,
		SupportsPersistentConfig: true,
		ProtocolPreference:       transport.OpenAI,
	}
}

func geminiDir(home string) string {
	if env := os.Getenv("GEMINI_CONFIG_DIR"); env != "" && home == liveHome() {
		return env
	}
	return filepath.Join(home, ".gemini")
}

func geminiEnvPath(home string) string      { return filepath.Join(geminiDir(home), ".env") }
func geminiSettingsPath(home string) string { return filepath.Join(geminiDir(home), "settings.json") }

func liveHome() string {
	home, _ := os.UserHomeDir()
	if env := os.Getenv("HOME"); env != "" {
		return env
	}
	return home
}

func (a *geminiAdapter) Apply(ctx *ApplyContext) (*ApplyResult, error) {
	targetHome := ctx.Home
	if ctx.IsolatedHome != "" {
		targetHome = ctx.IsolatedHome
	}

	env := map[string]string{}
	if ctx.APIKey != "" {
		env["GEMINI_API_KEY"] = ctx.APIKey
	}
	if ctx.Endpoint != "" {
		// Best-effort: some forks / gateways honor a base URL override.
		env["GOOGLE_GEMINI_BASE_URL"] = ctx.Endpoint
		env["GEMINI_API_BASE_URL"] = ctx.Endpoint
	}
	if defaultModel := firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel); defaultModel != "" {
		env["GEMINI_MODEL"] = defaultModel
	}
	for k, v := range ctx.ExtraEnv {
		if v == "" {
			continue
		}
		if strings.HasPrefix(k, "GEMINI_") || strings.HasPrefix(k, "GOOGLE_") ||
			k == "GOOGLE_APPLICATION_CREDENTIALS" {
			env[k] = v
		}
	}

	targetEnv := geminiEnvPath(targetHome)
	targetSettings := geminiSettingsPath(targetHome)
	managedPaths := []string{targetEnv, targetSettings}
	managedEnvKeys := sortedKeys(env)

	if ctx.DryRun {
		managedPaths = append(managedPaths,
			filepath.Join(ctx.AnyPickRoot, "clients", "gemini", "env.sh"),
			filepath.Join(ctx.AnyPickRoot, "clients", "gemini", "env.ps1"))
		return &ApplyResult{ManagedPaths: managedPaths, ManagedEnvKeys: managedEnvKeys}, nil
	}

	// Existing user config is authoritative. A malformed JSON file must never
	// be replaced with an empty object — fail before touching anything.
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(targetSettings); err == nil {
		var parsed map[string]json.RawMessage
		if json.Unmarshal(raw, &parsed) != nil || !jsonLooksObject(raw) {
			return nil, anyerr.Newf(
				fmt.Sprintf("Refusing to overwrite unreadable Gemini settings at %s: root value is not an object", targetSettings),
				"CLIENT_CONFIG_INVALID")
		}
		doc = parsed
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var envPaths []string
	if ctx.IsolatedHome == "" {
		envPaths = writeClientEnvFiles(ctx.AnyPickRoot, "gemini", env)
	}

	if err := os.MkdirAll(geminiDir(targetHome), 0o700); err != nil {
		return nil, err
	}
	if err := upsertEnvFile(targetEnv, env); err != nil {
		return nil, err
	}

	marker, _ := json.Marshal(map[string]interface{}{"keys": managedEnvKeys})
	doc[anypickManagedKey] = marker
	out, err := json.MarshalIndent(map[string]json.RawMessage(doc), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(targetSettings, append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	managedPaths = append(managedPaths, envPaths...)
	return &ApplyResult{ManagedPaths: managedPaths, ManagedEnvKeys: managedEnvKeys}, nil
}

// jsonLooksObject guards against arrays/scalars parsed into a non-map type.
func jsonLooksObject(raw []byte) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

func (a *geminiAdapter) Reset() error {
	liveEnv := geminiEnvPath(a.home)
	keys, _ := readManagedMarkerKeys(geminiSettingsPath(a.home))
	if len(keys) == 0 {
		// Fall back to the keys this adapter owns by construction.
		keys = []string{"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_BASE_URL", "GEMINI_MODEL"}
	}
	if _, err := os.Stat(liveEnv); err == nil {
		if err := stripKeysFromEnv(liveEnv, keys); err != nil {
			return err
		}
	}
	if raw, err := os.ReadFile(geminiSettingsPath(a.home)); err == nil {
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) == nil {
			delete(doc, anypickManagedKey)
			if out, merr := json.MarshalIndent(map[string]json.RawMessage(doc), "", "  "); merr == nil {
				_ = os.WriteFile(geminiSettingsPath(a.home), append(out, '\n'), 0o600)
			}
		}
	}
	removeClientEnvFiles(anyPickRootFallback(), "gemini")
	return nil
}

func (a *geminiAdapter) Inspect() (*InspectResult, error) {
	_, envErr := os.Stat(geminiEnvPath(a.home))
	_, setErr := os.Stat(geminiSettingsPath(a.home))
	present := envErr == nil || setErr == nil
	res := &InspectResult{Present: present}
	if setErr == nil {
		keys, err := readManagedMarkerKeys(geminiSettingsPath(a.home))
		if err != nil {
			res.Issues = []string{"settings.json unreadable"}
			return res, nil
		}
		if len(keys) > 0 {
			res.Summary = fmt.Sprintf("anypick-managed env keys: %d", len(keys))
		} else {
			res.Summary = "settings present"
		}
	}
	return res, nil
}

// readManagedMarkerKeys extracts _anypickManaged.keys from a settings file.
func readManagedMarkerKeys(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Managed struct {
			Keys []string `json:"keys"`
		} `json:"_anypickManaged"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, fmt.Errorf("unreadable settings")
	}
	return doc.Managed.Keys, nil
}

// upsertEnvFile writes KEY=VALUE lines for every entry, preserving unrelated
// lines and comments in the existing file (ports providers/gemini upsertEnvFile).
func upsertEnvFile(path string, env map[string]string) error {
	existing, _ := os.ReadFile(path)
	lines := strings.Split(string(existing), "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key := trimmed
		if eq := strings.Index(trimmed, "="); eq >= 0 {
			key = strings.TrimSpace(trimmed[:eq])
		}
		if v, ok := env[key]; ok {
			lines[i] = fmt.Sprintf("%s=%s", key, quoteEnvValue(v))
			seen[key] = true
		}
	}
	for _, k := range sortedKeys(env) {
		if !seen[k] {
			lines = append(lines, fmt.Sprintf("%s=%s", k, quoteEnvValue(env[k])))
		}
	}
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	return os.WriteFile(path, []byte(out+"\n"), 0o600)
}

func quoteEnvValue(v string) string {
	if v == "" {
		return ""
	}
	if strings.ContainsAny(v, " \t\"'#$\\") {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return v
}

// stripKeysFromEnv rewrites an .env file without the given keys.
func stripKeysFromEnv(path string, keys []string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		key := trimmed
		if eq := strings.Index(trimmed, "="); eq >= 0 {
			key = strings.TrimSpace(trimmed[:eq])
		}
		if drop[key] {
			continue
		}
		out = append(out, line)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(strings.Join(out, "\n"), "\n")+"\n"), 0o600)
}

func removeClientEnvFiles(root, clientID string) {
	dir := filepath.Join(root, "clients", clientID)
	_ = os.Remove(filepath.Join(dir, "env.sh"))
	_ = os.Remove(filepath.Join(dir, "env.ps1"))
}

// anyPickRootFallback mirrors the TS reset fallback chain.
func anyPickRootFallback() string {
	if env := os.Getenv("ANYPICK_HOME"); env != "" {
		return env
	}
	return filepath.Join(liveHome(), ".anypick")
}
