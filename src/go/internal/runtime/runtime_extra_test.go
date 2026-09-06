package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/profile"
)

func TestApplyProxyEndpointWritesClaudeEnv(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	res, err := s.ApplyProxyEndpoint(ctx, "claude", ProxyEndpointOptions{
		Endpoint: "https://proxy.example/v1",
		APIKey:   "sk-proxy",
	})
	if err != nil {
		t.Fatalf("ApplyProxyEndpoint claude: %v", err)
	}
	if len(res.ManagedPaths) == 0 {
		t.Fatal("ManagedPaths empty")
	}
	env := readSettingsEnv(t, filepath.Join(s.Home, ".claude", "settings.json"))
	if env["ANTHROPIC_BASE_URL"] != "https://proxy.example/v1" {
		t.Fatalf("ANTHROPIC_BASE_URL=%q", env["ANTHROPIC_BASE_URL"])
	}
	// Codex sibling should not have been touched yet.
	if _, err := os.Stat(codexConfigPath(s)); !os.IsNotExist(err) {
		t.Fatalf("codex config should not exist after claude apply")
	}
}

func TestApplyProxyEndpointWritesCodexBlock(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	t.Setenv("CODEX_HOME", "")
	res, err := s.ApplyProxyEndpoint(ctx, "codex", ProxyEndpointOptions{
		Endpoint:     "https://proxy.example/v1",
		APIKey:       "sk-proxy",
		DefaultModel: "gpt-5",
	})
	if err != nil {
		t.Fatalf("ApplyProxyEndpoint codex: %v", err)
	}
	if len(res.ManagedPaths) == 0 {
		t.Fatal("ManagedPaths empty")
	}
	raw, err := os.ReadFile(codexConfigPath(s))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "AnyPick") || !contains(string(raw), "https://proxy.example/v1") {
		t.Fatalf("codex config missing proxy block:\n%s", raw)
	}
}

func TestApplyGatewayProfileErrors(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	// Unknown profile.
	if _, err := s.ApplyGatewayProfile(ctx, "nope", "claude"); err == nil {
		t.Fatal("want error for missing gateway")
	}
	// Profile without endpoint.
	if _, err := s.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta: profile.Meta{Name: "empty", Provider: "openrouter"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGatewayProfile(ctx, "empty", "claude"); err == nil {
		t.Fatal("want error for gateway without endpoint")
	}
	// Unknown client.
	if _, err := s.ApplyGatewayProfile(ctx, "empty", "nope-client"); err == nil {
		t.Fatal("want error for unknown client")
	}
}

func TestPrepareAndRestoreRecovery(t *testing.T) {
	s := testService(t)
	// Seed a file that recovery should capture.
	claudeSettings := filepath.Join(s.Home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := `{"env":{"FOO":"bar"}}`
	if err := os.WriteFile(claudeSettings, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := s.PrepareClientConfigRecovery("claude", "journal-123")
	if err != nil || len(records) == 0 {
		t.Fatalf("Prepare records=%v err=%v", records, err)
	}
	// Mutate live file, then restore via records.
	if err := os.WriteFile(claudeSettings, []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreClientConfigRecovery(records); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(claudeSettings)
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(got, &doc)
	if _, ok := doc["env"]; !ok {
		t.Fatalf("restored file missing env: %s", got)
	}

	// Missing-file case produces delete=> record and Restore removes file.
	s2 := testService(t)
	records2, err := s2.PrepareClientConfigRecovery("claude", "jid-2")
	if err != nil {
		t.Fatal(err)
	}
	foundDelete := false
	for _, r := range records2 {
		if len(r) >= 8 && r[:8] == "delete=>" {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("want delete=> record for missing file, got %v", records2)
	}
	// Create file then restore delete record should remove it.
	dummy := filepath.Join(s2.Home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(dummy), 0o700)
	_ = os.WriteFile(dummy, []byte(`{}`), 0o600)
	if err := RestoreClientConfigRecovery(records2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dummy); !os.IsNotExist(err) {
		t.Fatalf("want file deleted after restore, stat err=%v", err)
	}
}

// TestApplyProxyEndpointWritesZCodeProvider covers the managed-proxy seam for
// ZCode. Unlike a gateway (direct) source, this path never gets exercised by a
// sandbox smoke test: it only runs after a real proxy has passed its health
// check, so a fake credential cannot reach it. The adapter's own tests call
// Apply directly and would not catch a ProxyEndpointOptions field this package
// forgot to forward.
func TestApplyProxyEndpointWritesZCodeProvider(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	cliPath := filepath.Join(s.Home, ".zcode", "cli", "config.json")
	v2Path := filepath.Join(s.Home, ".zcode", "v2", "config.json")
	for _, p := range []string{cliPath, v2Path} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"provider":{"zai":{"name":"Z.AI"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.ApplyProxyEndpoint(ctx, "zcode", ProxyEndpointOptions{
		Endpoint:     "http://127.0.0.1:8080",
		APIKey:       "sk-proxy",
		DefaultModel: "glm-4.6",
	})
	if err != nil {
		t.Fatalf("ApplyProxyEndpoint zcode: %v", err)
	}
	// The checkpoint is reported too — opencode does the same, and Reset needs
	// it to know whether it created a file or merely edited one.
	managed := map[string]bool{}
	for _, p := range res.ManagedPaths {
		managed[p] = true
	}
	if !managed[cliPath] || !managed[v2Path] {
		t.Fatalf("ManagedPaths=%v, want both ZCode configs", res.ManagedPaths)
	}

	for _, p := range []string{cliPath, v2Path} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Provider map[string]struct {
				Name    string `json:"name"`
				Options struct {
					BaseURL string `json:"baseURL"`
					APIKey  string `json:"apiKey"`
				} `json:"options"`
				Models map[string]any `json:"models"`
			} `json:"provider"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		entry, ok := doc.Provider["anypick"]
		if !ok {
			t.Fatalf("%s: managed provider missing:\n%s", p, raw)
		}
		if entry.Options.BaseURL != "http://127.0.0.1:8080" {
			t.Fatalf("%s: baseURL=%q", p, entry.Options.BaseURL)
		}
		if entry.Options.APIKey != "sk-proxy" {
			t.Fatalf("%s: apiKey=%q", p, entry.Options.APIKey)
		}
		if _, ok := entry.Models["glm-4.6"]; !ok {
			t.Fatalf("%s: model glm-4.6 missing:\n%s", p, raw)
		}
		// The pre-existing provider must survive the merge.
		if _, ok := doc.Provider["zai"]; !ok {
			t.Fatalf("%s: user provider zai was dropped:\n%s", p, raw)
		}
	}

	// Only the CLI flavour has no separate selection file, so only it gets the
	// top-level model rewrite.
	raw, err := os.ReadFile(cliPath)
	if err != nil {
		t.Fatal(err)
	}
	var cliDoc struct {
		Model map[string]string `json:"model"`
	}
	if err := json.Unmarshal(raw, &cliDoc); err != nil {
		t.Fatal(err)
	}
	if cliDoc.Model["main"] != "anypick/glm-4.6" {
		t.Fatalf("cli model.main=%q, want anypick/glm-4.6", cliDoc.Model["main"])
	}
}

// TestPrepareZCodeRecoveryKeepsBothConfigsDistinct covers the case the shared
// basename breaks: ZCode keeps a config.json under both ~/.zcode/cli and
// ~/.zcode/v2, so a backup-name collision would restore one flavour's content
// into the other's path during crash recovery.
func TestPrepareZCodeRecoveryKeepsBothConfigsDistinct(t *testing.T) {
	s := testService(t)
	cliPath := filepath.Join(s.Home, ".zcode", "cli", "config.json")
	v2Path := filepath.Join(s.Home, ".zcode", "v2", "config.json")
	for _, p := range []string{cliPath, v2Path} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cliPath, []byte(`{"cli":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2Path, []byte(`{"v2":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	records, err := s.PrepareClientConfigRecovery("zcode", "journal-123")
	if err != nil || len(records) != 2 {
		t.Fatalf("Prepare records=%v err=%v, want one per config file", records, err)
	}
	dests := map[string]string{}
	for _, r := range records {
		parts := strings.SplitN(r, "=>", 2)
		if len(parts) != 2 {
			t.Fatalf("record %q is not src=>dest", r)
		}
		dests[parts[0]] = parts[1]
	}
	if dests[cliPath] == dests[v2Path] {
		t.Fatalf("both configs backed up to the same file %q", dests[cliPath])
	}

	// Corrupt both, restore, and confirm each file got its own content back.
	for _, p := range []string{cliPath, v2Path} {
		if err := os.WriteFile(p, []byte(`{"corrupt":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RestoreClientConfigRecovery(records); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cliPath); strings.TrimSpace(string(got)) != `{"cli":true}` {
		t.Fatalf("cli config restored to %s", got)
	}
	if got, _ := os.ReadFile(v2Path); strings.TrimSpace(string(got)) != `{"v2":true}` {
		t.Fatalf("v2 config restored to %s", got)
	}
}

func TestPrepareRecoveryFailsWhenSourceCannotBeRead(t *testing.T) {
	s := testService(t)
	settings := filepath.Join(s.Home, ".claude", "settings.json")
	if err := os.MkdirAll(settings, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PrepareClientConfigRecovery("claude", "journal-read-error"); err == nil {
		t.Fatal("want unreadable source config to abort recovery preparation")
	}
}

func TestPrepareRecoveryFailsWhenBackupCannotBeWritten(t *testing.T) {
	s := testService(t)
	settings := filepath.Join(s.Home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"KEEP":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(
		s.Root,
		"recovery",
		"clients",
		"claude",
		"settings.json.journal-write-error.bak",
	)
	if err := os.MkdirAll(backupPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PrepareClientConfigRecovery("claude", "journal-write-error"); err == nil {
		t.Fatal("want backup write failure to abort recovery preparation")
	}
}

func TestSmartEndpointCatalogAuthoritative(t *testing.T) {
	// OpenRouter entry from catalog carries AnthropicEndpoint that wins over heuristic.
	s := testService(t)
	ctx := context.Background()
	if _, err := s.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "or-work2", Provider: "openrouter", Endpoint: "https://openrouter.ai/api/v1"},
		Secrets: profile.Secrets{APIKey: "sk-or"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGatewayProfile(ctx, "or-work2", "claude"); err != nil {
		t.Fatal(err)
	}
	env := readSettingsEnv(t, filepath.Join(s.Home, ".claude", "settings.json"))
	if env["ANTHROPIC_BASE_URL"] != "https://openrouter.ai/api" {
		t.Fatalf("catalog authoritative AnthropicEndpoint not used: %q", env["ANTHROPIC_BASE_URL"])
	}
}
