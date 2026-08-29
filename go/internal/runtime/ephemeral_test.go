package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateEphemeralClaude(t *testing.T) {
	s := testService(t)
	// Seed a live settings file so copyIfPresent has something to carry.
	liveSettings := filepath.Join(s.Home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(liveSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveSettings, []byte(`{"env":{"KEEP":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := s.CreateEphemeralRuntime(EphemeralSpec{
		ClientID: "claude",
		Endpoint: "https://proxy.example/v1",
		APIKey:   "sk-proxy",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralRuntime claude: %v", err)
	}
	defer func() { _ = rt.Cleanup() }()
	if rt.Home == "" || rt.Directory == "" {
		t.Fatalf("EphemeralRuntime empty: %#v", rt)
	}
	if rt.Environment["HOME"] != rt.Home {
		t.Fatalf("claude HOME env = %q want %q", rt.Environment["HOME"], rt.Home)
	}
	// Isolated settings should exist and contain proxy env.
	isoSettings := filepath.Join(rt.Home, ".claude", "settings.json")
	raw, err := os.ReadFile(isoSettings)
	if err != nil {
		t.Fatalf("iso settings missing: %v", err)
	}
	if !strings.Contains(string(raw), "proxy.example") {
		t.Fatalf("iso settings missing proxy: %s", raw)
	}
	// Cleanup is idempotent.
	if err := rt.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(rt.Directory); !os.IsNotExist(err) {
		t.Fatalf("directory should be removed after Cleanup")
	}
	_ = rt.Cleanup() // second call must not error
}

func TestCreateEphemeralCodex(t *testing.T) {
	s := testService(t)
	t.Setenv("CODEX_HOME", "")
	// Seed a codex config so copyIfPresent copies it.
	cfg := filepath.Join(s.Home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("# user\nmodel=\"user-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := s.CreateEphemeralRuntime(EphemeralSpec{
		ClientID:     "codex",
		Endpoint:     "https://proxy.example/v1",
		APIKey:       "sk-proxy",
		DefaultModel: "gpt-5",
	})
	if err != nil {
		t.Fatalf("CreateEphemeralRuntime codex: %v", err)
	}
	defer func() { _ = rt.Cleanup() }()
	if rt.Environment["CODEX_HOME"] == "" {
		t.Fatalf("codex CODEX_HOME env missing: %#v", rt.Environment)
	}
	if rt.Environment["ANYPICK_PROXY_API_KEY"] != "sk-proxy" {
		t.Fatalf("APIKey env = %q", rt.Environment["ANYPICK_PROXY_API_KEY"])
	}
	isoCfg := filepath.Join(rt.Home, ".codex", "config.toml")
	raw, err := os.ReadFile(isoCfg)
	if err != nil {
		t.Fatal(err)
	}
	// Should preserve user content and add proxy block.
	if !strings.Contains(string(raw), "user-model") {
		t.Fatalf("user content lost: %s", raw)
	}
	if !strings.Contains(string(raw), "proxy.example") {
		t.Fatalf("proxy block missing: %s", raw)
	}
}

func TestCreateEphemeralUnknownClient(t *testing.T) {
	s := testService(t)
	if _, err := s.CreateEphemeralRuntime(EphemeralSpec{ClientID: "nope", Endpoint: "https://x", APIKey: "sk"}); err == nil {
		t.Fatal("want error for unknown client")
	}
	// Known provider id that doesn't support isolated homes would also error if registered,
	// but "gemini" is not a client adapter — unknown is enough to cover the branch.
}

func TestCreateEphemeralCodexWithoutSourceFile(t *testing.T) {
	s := testService(t)
	t.Setenv("CODEX_HOME", "")
	// Ensure no source config exists — copyIfPresent should be no-op.
	_ = os.Remove(filepath.Join(s.Home, ".codex", "config.toml"))
	rt, err := s.CreateEphemeralRuntime(EphemeralSpec{ClientID: "codex", Endpoint: "https://proxy.example/v1", APIKey: "sk"})
	if err != nil {
		t.Fatalf("codex without source: %v", err)
	}
	defer func() { _ = rt.Cleanup() }()
	isoCfg := filepath.Join(rt.Home, ".codex", "config.toml")
	if _, err := os.Stat(isoCfg); err != nil {
		t.Fatalf("iso config should exist after Apply even without source: %v", err)
	}
}

func TestHelpersSanitizeAndSplit(t *testing.T) {
	if got := sanitize("hello-world_123"); got != "hello-world_123" {
		t.Fatalf("sanitize alnum = %q", got)
	}
	if got := sanitize("a b/c@d"); got != "a_b_c_d" {
		t.Fatalf("sanitize special = %q", got)
	}
	long := strings.Repeat("a", 30)
	if len(sanitize(long)) != 24 {
		t.Fatalf("sanitize trunc len = %d", len(sanitize(long)))
	}
	// splitRecord
	src, dest, ok := splitRecord("a=>b")
	if !ok || src != "a" || dest != "b" {
		t.Fatalf("splitRecord a=>b = %q %q %v", src, dest, ok)
	}
	src, dest, ok = splitRecord(" delete => /tmp/x ")
	if !ok || src != "delete" || dest != "/tmp/x" {
		t.Fatalf("splitRecord spaced = %q %q %v", src, dest, ok)
	}
	if _, _, ok := splitRecord("nope"); ok {
		t.Fatalf("splitRecord should fail for missing =>")
	}
	if got := trimSpace(" \t hi \t "); got != "hi" {
		t.Fatalf("trimSpace = %q", got)
	}
	// copyIfPresent: missing src is no error and dest not created
	tmp := t.TempDir()
	if err := copyIfPresent(filepath.Join(tmp, "missing"), filepath.Join(tmp, "dest")); err != nil {
		t.Fatalf("copyIfPresent missing source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "dest")); !os.IsNotExist(err) {
		t.Fatalf("copyIfPresent should not create dest for missing src")
	}
	// present src copies
	srcFile := filepath.Join(tmp, "src.txt")
	destFile := filepath.Join(tmp, "dest.txt")
	_ = os.WriteFile(srcFile, []byte("hello"), 0o600)
	if err := copyIfPresent(srcFile, destFile); err != nil {
		t.Fatalf("copyIfPresent: %v", err)
	}
	raw, _ := os.ReadFile(destFile)
	if string(raw) != "hello" {
		t.Fatalf("copyIfPresent failed: %q", raw)
	}
	// clientHome fallback and codexConfigPath env override
	s := testService(t)
	t.Setenv("CODEX_HOME", "/tmp/custom-codex")
	if got := codexConfigPath(s); got != "/tmp/custom-codex/config.toml" {
		t.Fatalf("codexConfigPath env = %q", got)
	}
	t.Setenv("CODEX_HOME", "")
	_ = clientHome(s) // just cover the branch
	// ApplyProxyEndpoint unknown client
	if _, err := s.ApplyProxyEndpoint(nil, "nope", ProxyEndpointOptions{Endpoint: "https://x", APIKey: "sk"}); err == nil {
		t.Fatal("want error for unknown client in ApplyProxyEndpoint")
	}
	// PrepareClientConfigRecovery unknown client -> error
	if _, err := s.PrepareClientConfigRecovery("nope", "jid"); err == nil {
		t.Fatalf("want error for unknown client in PrepareClientConfigRecovery")
	}
	// Restore with malformed record is ignored
	if err := RestoreClientConfigRecovery([]string{"bad-record"}); err != nil {
		t.Fatalf("malformed record should be ignored: %v", err)
	}
}

func TestCreateEphemeralRuntimeRejectsUnreadableSourceConfig(t *testing.T) {
	s := testService(t)
	settings := filepath.Join(s.Home, ".claude", "settings.json")
	if err := os.MkdirAll(settings, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateEphemeralRuntime(EphemeralSpec{
		ClientID: "claude",
		Endpoint: "https://proxy.example/v1",
		APIKey:   "sk-proxy",
	}); err == nil {
		t.Fatal("want an error when the source config cannot be read")
	}
}
