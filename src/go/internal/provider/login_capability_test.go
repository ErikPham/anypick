package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// declaredLogin is what a provider is allowed to advertise as a browser
// sign-in method.
type declaredLogin struct {
	methods    []string
	provenance string
}

// declaredLoginMethods is an allowlist, not documentation. Every registered
// provider is walked below: implementing LoginMethodsProvider without an entry
// here fails, and an entry whose provider stopped implementing it fails. A new
// login path is therefore always a reviewed change rather than something that
// appears in the UI because a provider grew a method.
var declaredLoginMethods = map[string]declaredLogin{
	"claude": {methods: []string{"claude-subscription"}, provenance: "official"},
	"codex":  {methods: []string{"chatgpt-subscription"}, provenance: "official"},
	"gemini": {
		methods:    []string{"gemini-subscription", "antigravity-subscription"},
		provenance: "official",
	},
	"grok": {methods: []string{"grok-subscription"}, provenance: "official"},
	"kiro": {methods: []string{"builder-id"}, provenance: "official"},
	// Two sites, one preferred: a token issued by one host is rejected by the
	// other. Community, not official — the device flow behind these is
	// undocumented and Tencent can change it without notice.
	"workbuddy": {
		methods:    []string{"workbuddy-international", "codebuddy-china"},
		provenance: "community",
	},
	"zai": {methods: []string{"zai-subscription"}, provenance: "official"},
	// One method: the WorkOS device flow is Cline's own, but reusing its
	// tokens from another harness is undocumented. Community, not official.
	"cline": {methods: []string{"cline-pass"}, provenance: "community"},
}

func TestLoginMethodsDeclaredPerProvider(t *testing.T) {
	reg := NewRegistry()
	home := t.TempDir()
	if err := RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	if err := RegisterBuiltinCompatibles(reg, home); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(declaredLoginMethods))
	for _, id := range reg.IDs() {
		p, err := reg.Get(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		mp, ok := p.(LoginMethodsProvider)
		want, declared := declaredLoginMethods[id]
		if !ok {
			if declared {
				t.Errorf("%s is declared with login methods but does not implement LoginMethodsProvider", id)
			}
			continue
		}
		if !declared {
			// Providers without browser sign-in must not advertise it.
			t.Errorf("%s implements LoginMethodsProvider but is not declared in declaredLoginMethods", id)
			continue
		}
		seen[id] = true
		methods := mp.LoginMethods()
		if len(methods) != len(want.methods) {
			t.Fatalf("%s methods = %+v, want %v", id, methods, want.methods)
		}
		preferred := 0
		for i, m := range methods {
			if m.ID != want.methods[i] {
				t.Errorf("%s method[%d].ID = %q, want %q", id, i, m.ID, want.methods[i])
			}
			if m.Provenance != want.provenance {
				t.Errorf("%s method[%d].Provenance = %q, want %q", id, i, m.Provenance, want.provenance)
			}
			if m.Preferred {
				preferred++
			}
		}
		// Exactly one, not "at least one": a UI with two defaults is a bug the
		// user has to resolve by guessing.
		if preferred != 1 {
			t.Errorf("%s must mark exactly one method preferred, got %d", id, preferred)
		}
	}
	for id := range declaredLoginMethods {
		if !seen[id] {
			t.Errorf("%s is declared but no such provider is registered", id)
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
