package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	if _, err := s.Profiles.Upsert(profile.Runtime{
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
	if _, err := s.Profiles.Upsert(profile.Runtime{
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
