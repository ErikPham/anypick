package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeApplyDryRun(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	reg := NewRegistry()
	if err := RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("claude")
	res, err := a.Apply(&ApplyContext{
		Endpoint:    "https://proxy.example/v1",
		APIKey:      "sk-proxy",
		Home:        home,
		AnyPickRoot: root,
		DryRun:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ManagedPaths) == 0 || len(res.ManagedEnvKeys) == 0 {
		t.Fatalf("ManagedPaths/EnvKeys empty: %#v", res)
	}
	// DryRun must not touch disk.
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("DryRun created settings.json")
	}
}

func TestClaudeApplyRealAndInspect(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	reg := NewRegistry()
	_ = RegisterBuiltin(reg, home)
	a, _ := reg.Get("claude")

	if _, err := a.Apply(&ApplyContext{
		Endpoint:    "https://proxy.example/v1",
		APIKey:      "sk-proxy",
		Home:        home,
		AnyPickRoot: root,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("settings.json unreadable: %s", raw)
	}
	if _, ok := doc["env"]; !ok {
		t.Fatalf("settings.json missing env: %s", raw)
	}
	if _, ok := doc[anypickManagedKey]; !ok {
		t.Fatalf("settings.json missing %s: %s", anypickManagedKey, raw)
	}
	ins, err := a.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !ins.Present {
		t.Fatalf("Inspect Present=false want true, issues=%v", ins.Issues)
	}
	// Managed env block must contain base url.
	var env map[string]string
	_ = json.Unmarshal(doc["env"], &env)
	if env["ANTHROPIC_BASE_URL"] != "https://proxy.example/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL=%q", env["ANTHROPIC_BASE_URL"])
	}
}

func TestClaudeApplyPreservesUserKeys(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	// Seed existing settings with user env key that should survive.
	claudeDir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(claudeDir, 0o700)
	seed := `{"env":{"MY_KEEP":"1","ANTHROPIC_MODEL":"old"}}`
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(seed), 0o600)

	reg := NewRegistry()
	_ = RegisterBuiltin(reg, home)
	a, _ := reg.Get("claude")
	if _, err := a.Apply(&ApplyContext{
		Endpoint:    "https://proxy.example/v1",
		APIKey:      "sk",
		Home:        home,
		AnyPickRoot: root,
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	var env map[string]string
	_ = json.Unmarshal(doc["env"], &env)
	if env["MY_KEEP"] != "1" {
		t.Fatalf("user key MY_KEEP lost: %v", env)
	}
}

func TestCodexApplyAndInspect(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	// Isolate CODEX_HOME to home.
	t.Setenv("CODEX_HOME", "")
	reg := NewRegistry()
	_ = RegisterBuiltin(reg, home)
	a, _ := reg.Get("codex")

	// No config yet -> Inspect reports absent.
	ins, _ := a.Inspect()
	if ins.Present {
		t.Fatalf("Inspect want absent before Apply")
	}

	// DryRun.
	if res, err := a.Apply(&ApplyContext{
		Endpoint:    "https://proxy.example/v1",
		APIKey:      "sk",
		Home:        home,
		AnyPickRoot: root,
		DryRun:      true,
	}); err != nil || len(res.ManagedPaths) == 0 {
		t.Fatalf("DryRun codex: %v %#v", err, res)
	}

	// Real apply.
	if _, err := a.Apply(&ApplyContext{
		Endpoint:    "https://proxy.example/v1",
		APIKey:      "sk",
		Home:        home,
		AnyPickRoot: root,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), codexBeginMarker) || !strings.Contains(string(raw), "AnyPick") {
		t.Fatalf("codex config missing managed block:\n%s", raw)
	}
	ins2, _ := a.Inspect()
	if !ins2.Present {
		t.Fatalf("Inspect after Apply want present")
	}

	// Reset removes block.
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if strings.Contains(string(raw2), codexBeginMarker) {
		t.Fatalf("Reset did not remove block:\n%s", raw2)
	}
}

func TestCodexPreservesUserContent(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	_ = os.WriteFile(cfgPath, []byte("# user header\nmodel = \"user-model\"\n"), 0o600)

	reg := NewRegistry()
	_ = RegisterBuiltin(reg, home)
	a, _ := reg.Get("codex")
	if _, err := a.Apply(&ApplyContext{Endpoint: "https://proxy.example/v1", APIKey: "sk", Home: home, AnyPickRoot: root}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "user-model") {
		t.Fatalf("user content lost:\n%s", raw)
	}
	if !strings.Contains(string(raw), codexBeginMarker) {
		t.Fatalf("managed block missing:\n%s", raw)
	}
}
