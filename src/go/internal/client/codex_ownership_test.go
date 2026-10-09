package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

func TestCodexApplyRefusesForeignRootModel(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newCodex(home, root)
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const userConfig = "model = \"user-pin\"\n\n[other]\nkey = 1\n"
	if err := os.WriteFile(configPath, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "k",
		DefaultModel: "proxy-model",
		AnyPickRoot:  root, Home: home,
	})
	if err == nil {
		t.Fatal("expected CLIENT_CONFIG_FOREIGN, apply succeeded")
	}
	aerr, ok := err.(*anyerr.Error)
	if !ok || aerr.Code != "CLIENT_CONFIG_FOREIGN" {
		t.Fatalf("want CLIENT_CONFIG_FOREIGN, got %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if string(raw) != userConfig {
		t.Fatalf("foreign config was modified before consent:\n%s", raw)
	}
}

func TestCodexNativeProfileReapplyIsOwned(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	root := t.TempDir()
	a := newCodex(home, root)
	snap := t.TempDir()
	if err := os.WriteFile(filepath.Join(snap, "auth.json"), []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &ApplyContext{
		Endpoint: "http://127.0.0.1:9100", DefaultModel: "proxy-model",
		AnyPickRoot: root, Home: home, ProfileName: "work",
		SourceKind: "account", TransportCapability: transport.Direct,
		NativeAuthSnapshotDir: snap,
	}
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("first native apply: %v", err)
	}
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("re-apply of an AnyPick native profile was refused: %v", err)
	}
}

func TestCodexApplyManagedBlockIsOwned(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newCodex(home, root)
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := "# BEGIN anypick-managed\nmodel = \"old\"\nmodel_provider = \"anypick\"\n# END anypick-managed\n"
	if err := os.WriteFile(configPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "k",
		DefaultModel: "proxy-model",
		AnyPickRoot:  root, Home: home,
	}); err != nil {
		t.Fatalf("managed-block rewrite refused: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "model = \"proxy-model\"") {
		t.Fatalf("managed block not updated:\n%s", raw)
	}
	if !strings.Contains(string(raw), "experimental_bearer_token = \"k\"") {
		t.Fatalf("managed block missing experimental_bearer_token:\n%s", raw)
	}
}

func TestCodexApplyConsentAllowsForeignAndCheckpoints(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newCodex(home, root)
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("model = \"user-pin\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "k",
		DefaultModel: "proxy-model",
		AnyPickRoot:  root, Home: home,
		AllowForeignConfig: true,
	}); err != nil {
		t.Fatalf("consented apply failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "clients", "codex", "restore.json")); err != nil {
		t.Fatalf("checkpoint missing after consented apply: %v", err)
	}
	// Second apply needs no consent: the checkpoint now owns the lines.
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9101", APIKey: "k",
		DefaultModel: "proxy-model",
		AnyPickRoot:  root, Home: home,
	}); err != nil {
		t.Fatalf("second apply without consent failed: %v", err)
	}
}

func TestCodexUnrelatedConfigUntouched(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newCodex(home, root)
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const userConfig = "model_reasoning_effort = \"max\"\n\n[other]\nkey = 1\n"
	if err := os.WriteFile(configPath, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9100", APIKey: "k",
		DefaultModel: "proxy-model",
		AnyPickRoot:  root, Home: home,
	}); err != nil {
		t.Fatalf("apply refused without foreign root keys: %v", err)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "model_reasoning_effort") {
		t.Fatalf("unrelated user settings dropped:\n%s", raw)
	}
}
