package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The account switch already restored the native files, so RestoreNative
// removes only the stale marker and the generated env files — never live keys.
func TestGeminiRestoreNativeKeepsNativeEnv(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	a := newGemini(home)

	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const nativeEnv = "GEMINI_API_KEY=native-key\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(nativeEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(map[string]interface{}{"keys": []string{"GOOGLE_GEMINI_BASE_URL"}})
	settings := `{"other":"keep","_anypickManaged":` + string(marker) + `}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	envDir := filepath.Join(root, "clients", "gemini")
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "env.sh"), []byte("export X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.RestoreNative(); err != nil {
		t.Fatalf("RestoreNative: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".env")); string(got) != nativeEnv {
		t.Fatalf(".env changed by unroute: %q", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("settings unreadable after unroute: %s", raw)
	}
	if _, ok := doc[anypickManagedKey]; ok {
		t.Fatalf("stale marker survived unroute: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(envDir, "env.sh")); !os.IsNotExist(err) {
		t.Fatalf("generated env.sh survived unroute: %v", err)
	}
}

// RestoreNative removes the managed routing but keeps the checkpoint, so a
// later Reset still restores the exact pre-proxy state.
func TestOpencodeRestoreNativeKeepsCheckpoint(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	a := newOpenCode(home)

	configPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const userConfig = `{"model":"user-pick","theme":"dark"}`
	if err := os.WriteFile(configPath, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "secret-key",
		DefaultModel: "m1", ModelRoles: map[string]string{"default": "m1"},
		AnyPickRoot: root, Home: home,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	checkpoint := filepath.Join(root, "clients", "opencode", "restore.json")

	if err := a.RestoreNative(); err != nil {
		t.Fatalf("RestoreNative: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	var doc map[string]interface{}
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("config unreadable after unroute: %s", raw)
	}
	if providers, _ := doc["provider"].(map[string]interface{}); providers != nil {
		if _, ok := providers[opencodeManagedProviderID]; ok {
			t.Fatalf("managed provider survived unroute: %s", raw)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", ".anypick-api-key")); !os.IsNotExist(err) {
		t.Fatalf("secret survived unroute: %v", err)
	}
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatalf("RestoreNative must not consume the Reset checkpoint: %v", err)
	}

	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	raw, _ = os.ReadFile(configPath)
	doc = map[string]interface{}{}
	if json.Unmarshal(raw, &doc) != nil || doc["model"] != "user-pick" || doc["theme"] != "dark" {
		t.Fatalf("Reset did not restore user config: %s", raw)
	}
}

// Kiro has no live files: the generated env files are the whole surface.
func TestKiroRestoreNativeRemovesEnvFiles(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	a := newKiro(home)

	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "k",
		AnyPickRoot: root, Home: home,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	envSh := filepath.Join(root, "clients", "kiro", "env.sh")
	if _, err := os.Stat(envSh); err != nil {
		t.Fatalf("Apply did not write %s: %v", envSh, err)
	}
	if err := a.RestoreNative(); err != nil {
		t.Fatalf("RestoreNative: %v", err)
	}
	if _, err := os.Stat(envSh); !os.IsNotExist(err) {
		t.Fatalf("generated env survived unroute: %v", err)
	}
}

// A product root that appeared after the checkpoint is not listed in it,
// but its managed entries must still go on Reset.
func TestWorkBuddyResetSweepsPostCheckpointFile(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)

	// First activation only sees the default root; the checkpoint covers it.
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	first := workbuddyPaths(t, home)[0]

	// A second product root appears later; the next Apply writes both.
	secondDir := filepath.Join(home, ".codebuddy")
	if err := os.MkdirAll(secondDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	second := filepath.Join(secondDir, "models.json")
	if entries := workbuddyEntries(readWorkBuddyFile(t, second)); len(entries) == 0 {
		t.Fatalf("second root has no managed entries")
	}

	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// The checkpointed file is removed (AnyPick created it); the late file
	// keeps existing with managed entries swept.
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("checkpointed file survived Reset: %v", err)
	}
	if entries := workbuddyEntries(readWorkBuddyFile(t, second)); len(entries) != 0 {
		t.Fatalf("late file kept managed entries: %+v", entries)
	}
}

// The desktop v2 config can appear after the CLI-first checkpoint; Reset
// must not leave the proxy provider behind in either file.
func TestZCodeResetSweepsPostCheckpointFile(t *testing.T) {
	home := t.TempDir()
	a := newZCode(home)
	ctx := zcodeApplyContext(t, home)

	cliPath := filepath.Join(home, ".zcode", "cli", "config.json")
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	v2Path := filepath.Join(home, ".zcode", "v2", "config.json")
	if err := os.MkdirAll(filepath.Dir(v2Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2Path, []byte(`{"provider":{"builtin":{"kind":"anthropic"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if _, ok := mustZCodeProviders(t, v2Path)[zcodeManagedProviderID]; !ok {
		t.Fatalf("second Apply did not write v2 provider")
	}

	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// CLI flavour restores its exact prior content (checkpointed).
	cliDoc := readZCodeFile(t, cliPath)
	if providers, err := zcodeProviders(cliDoc); err != nil || len(providers) != 0 {
		t.Fatalf("cli providers after Reset = %v, %v", providers, err)
	}
	// The late v2 file keeps the user's provider with ours swept.
	v2Providers := mustZCodeProviders(t, v2Path)
	if _, ok := v2Providers[zcodeManagedProviderID]; ok {
		t.Fatalf("v2 kept managed provider: %v", v2Providers)
	}
	if _, ok := v2Providers["builtin"]; !ok {
		t.Fatalf("v2 lost user provider: %v", v2Providers)
	}
}

func mustZCodeProviders(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	providers, err := zcodeProviders(readZCodeFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return providers
}

// A root `model` pin Apply stripped for the managed block must come back on
// Reset instead of being silently dropped.
func TestCodexResetRestoresUserRootModel(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	t.Setenv("CODEX_HOME", "")
	a := newCodex(home)

	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const userConfig = "model = \"user-pin\"\n\n[other]\nkey = 1\n"
	if err := os.WriteFile(configPath, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "k",
		DefaultModel: "proxy-model",
		ModelCatalog: []string{"proxy-model"},
		AnyPickRoot:  root, Home: home,
		// The user pin is foreign on first apply; this test covers the
		// checkpoint/restore round-trip, so grant takeover.
		AllowForeignConfig: true,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	applied, _ := os.ReadFile(configPath)
	if !strings.Contains(string(applied), codexBeginMarker) {
		t.Fatalf("managed block missing:\n%s", applied)
	}
	if strings.Contains(string(applied), `model = "user-pin"`) {
		t.Fatalf("user pin was not stripped (invalid TOML risk):\n%s", applied)
	}
	if _, err := os.Stat(filepath.Join(root, "clients", "codex", "restore.json")); err != nil {
		t.Fatalf("checkpoint missing: %v", err)
	}

	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	restored, _ := os.ReadFile(configPath)
	if strings.Contains(string(restored), codexBeginMarker) {
		t.Fatalf("managed block survived Reset:\n%s", restored)
	}
	if !strings.Contains(string(restored), `model = "user-pin"`) {
		t.Fatalf("user pin was not restored:\n%s", restored)
	}
	if !strings.Contains(string(restored), "key = 1") {
		t.Fatalf("user table lost:\n%s", restored)
	}
}
