package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestLoginMethodsDeclaredPerProvider(t *testing.T) {
	reg := NewRegistry()
	home := t.TempDir()
	if err := RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"claude": "claude-subscription",
		"codex":  "chatgpt-subscription",
		"grok":   "grok-subscription",
		"kiro":   "builder-id",
	}
	for id, methodID := range want {
		p, err := reg.Get(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		mp, ok := p.(LoginMethodsProvider)
		if !ok {
			t.Fatalf("%s does not implement LoginMethodsProvider", id)
		}
		methods := mp.LoginMethods()
		if len(methods) != 1 || methods[0].ID != methodID {
			t.Fatalf("%s methods = %+v", id, methods)
		}
		if methods[0].Provenance != "official" || !methods[0].Preferred {
			t.Fatalf("%s provenance/preferred wrong: %+v", id, methods[0])
		}
	}
	// Providers without browser sign-in must not advertise it.
	for _, id := range []string{"gemini", "opencode"} {
		p, _ := reg.Get(id)
		if _, ok := p.(LoginMethodsProvider); ok {
			t.Fatalf("%s must not implement LoginMethodsProvider yet", id)
		}
	}
}

func TestCodexLoginRejectsUnknownMethodOrSource(t *testing.T) {
	p := newCodex(t.TempDir())
	err := p.Login(context.Background(), "wrong", "")
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("unknown method: %v", err)
	}
	err = p.Login(context.Background(), codexLoginMethodID, "other")
	if !errors.As(err, &ae) || ae.Code != "PROVIDER_LOGIN_METHOD_UNKNOWN" {
		t.Fatalf("foreign source: %v", err)
	}
}

func TestInspectCodexCredentialStore(t *testing.T) {
	codexHome := t.TempDir()
	status := InspectCodexCredentialStore(codexHome)
	if status.Mode != CodexStoreFile || !status.SupportsSnapshotSwitch {
		t.Fatalf("default must be file: %+v", status)
	}
	writeConfig := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(`cli_auth_credentials_store = "keyring"` + "\n")
	status = InspectCodexCredentialStore(codexHome)
	if status.Mode != CodexStoreKeyring || status.SupportsSnapshotSwitch {
		t.Fatalf("keyring: %+v", status)
	}
	if err := AssertFileCredentialStore(codexHome); err == nil {
		t.Fatal("keyring must refuse")
	} else {
		var ae *anyerr.Error
		if !errors.As(err, &ae) || ae.Code != "CODEX_CREDENTIAL_STORE_UNSUPPORTED" {
			t.Fatalf("code = %v", err)
		}
	}
	writeConfig("[mcp_servers.x]\ncli_auth_credentials_store = \"keyring\"\n")
	status = InspectCodexCredentialStore(codexHome)
	if status.Mode != CodexStoreFile {
		t.Fatalf("nested table value must be ignored: %+v", status)
	}
	writeConfig(`cli_auth_credentials_store = 'auto'` + "\n")
	status = InspectCodexCredentialStore(codexHome)
	if status.Mode != CodexStoreAuto || status.SupportsSnapshotSwitch {
		t.Fatalf("auto is deliberately not file: %+v", status)
	}
}

func TestCodexDetectLiveGatedByCredentialStore(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte(`cli_auth_credentials_store = "auto"`), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath,
		[]byte(`{"OPENAI_API_KEY":"sk-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := newCodex(t.TempDir()).DetectLive()
	if err != nil {
		t.Fatal(err)
	}
	if live.Present {
		t.Fatalf("non-file store must not report a live login: %+v", live)
	}
	if live.Details == "" {
		t.Fatal("remediation details missing")
	}
}

func TestClearLiveProvidersRemoveLocalFiles(t *testing.T) {
	home := t.TempDir()
	g := newGrok(home)
	authPath := g.authPath()
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.ClearLive(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("grok auth.json survived clear: %v", err)
	}

	c := newCodex(home)
	codexAuth := c.authPath()
	if err := os.MkdirAll(filepath.Dir(codexAuth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexAuth, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.ClearLive(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(codexAuth); !os.IsNotExist(err) {
		t.Fatalf("codex auth.json survived clear: %v", err)
	}

	gem := newGemini(home)
	envPath := gem.livePath(".env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=secret\nOTHER=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gem.livePath("oauth_creds.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gem.ClearLive(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gem.livePath("oauth_creds.json")); !os.IsNotExist(err) {
		t.Fatal("oauth creds survived clear")
	}
	data, rerr := os.ReadFile(envPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(data) != "OTHER=keep\n" {
		t.Fatalf(".env not stripped auth-only: %q", data)
	}
}

func TestCodexFileCredentialStoreGatesAllMutations(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte(`cli_auth_credentials_store = "keyring"`), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"sk-live"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newCodex(t.TempDir())
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "auth.json"),
		[]byte(`{"OPENAI_API_KEY":"sk-snapshot"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Restore refuses and must not touch the live file.
	if err := p.Restore(srcDir); err == nil {
		t.Fatal("restore must refuse under keyring store")
	}
	data, rerr := os.ReadFile(authPath)
	if rerr != nil || string(data) != `{"OPENAI_API_KEY":"sk-live"}` {
		t.Fatalf("restore mutated live auth despite refusal: %s", data)
	}

	// Backup refuses too.
	if _, err := p.Backup(t.TempDir()); err == nil {
		t.Fatal("backup must refuse under keyring store")
	}

	// Preflight refuses before any checkpoint is taken.
	var pr PreflightRestorer = p
	if err := pr.PreflightRestore(srcDir); err == nil {
		t.Fatal("preflight must refuse under keyring store")
	}

	// With the file store the same operations succeed.
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte(`cli_auth_credentials_store = "file"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Restore(srcDir); err != nil {
		t.Fatalf("restore under file store: %v", err)
	}
	data, _ = os.ReadFile(authPath)
	if string(data) != `{"OPENAI_API_KEY":"sk-snapshot"}` {
		t.Fatalf("restore did not apply: %s", data)
	}
}
