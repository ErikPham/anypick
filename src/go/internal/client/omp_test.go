package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOmpApplyWritesManagedProvider(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newOmp(home)

	res, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk-test",
		DefaultModel: "gpt-5.6",
		ModelCatalog: []string{"gpt-5.6", "o3"},
		AnyPickRoot:  root,
		Home:         home,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(mustRead(t, ompModelsPath(home)))
	for _, want := range []string{
		ompModelsBegin,
		"anypick:",
		"baseUrl: http://127.0.0.1:9000/v1",
		"apiKey: sk-test",
		"api: openai-completions",
		"- id: 'gpt-5.6'",
		ompModelsEnd,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("models.yml missing %q:\n%s", want, raw)
		}
	}
	cfgRaw := string(mustRead(t, ompConfigPath(home)))
	for _, want := range []string{
		ompConfigBegin,
		"defaultProvider: anypick",
		"defaultModel: 'anypick/gpt-5.6'",
		"model: 'anypick/gpt-5.6'",
		"modelRoles:",
		"  default: 'anypick/gpt-5.6'",
		ompConfigEnd,
	} {
		if !strings.Contains(cfgRaw, want) {
			t.Errorf("config.yml missing %q:\n%s", want, cfgRaw)
		}
	}
	if len(res.ManagedPaths) != 2 || res.ManagedPaths[0] != ompModelsPath(home) || res.ManagedPaths[1] != ompConfigPath(home) {
		t.Errorf("managed paths = %v", res.ManagedPaths)
	}
}

func TestOmpApplyRequiresEndpoint(t *testing.T) {
	home := t.TempDir()
	a := newOmp(home)
	if _, err := a.Apply(&ApplyContext{
		APIKey: "sk-test", AnyPickRoot: t.TempDir(), Home: home,
	}); err == nil {
		t.Fatal("missing endpoint must fail")
	}
}

func TestOmpApplyPreservesUserContent(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "providers:\n  openrouter:\n    baseUrl: https://openrouter.ai/api/v1\n    apiKey: sk-or\n# user comment\n"
	if err := os.WriteFile(ompModelsPath(home), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", AnyPickRoot: t.TempDir(), Home: home,
	}); err != nil {
		t.Fatal(err)
	}
	raw := string(mustRead(t, ompModelsPath(home)))
	for _, want := range []string{
		"openrouter:", "baseUrl: https://openrouter.ai/api/v1", "# user comment",
		ompModelsBegin, "anypick:",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("user content lost, missing %q:\n%s", want, raw)
		}
	}
}

func TestOmpApplyRefusesForeignAnypickKey(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "providers:\n  anypick:\n    baseUrl: https://user.example/v1\n"
	if err := os.WriteFile(ompModelsPath(home), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", AnyPickRoot: t.TempDir(), Home: home,
	}); err == nil {
		t.Fatal("user-owned anypick: key must fail closed, never be duplicated")
	}
}

func TestOmpApplyRejectsUnbalancedMarkers(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ompModelsPath(home), []byte("providers:\n  "+ompModelsBegin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", AnyPickRoot: t.TempDir(), Home: home,
	}); err == nil {
		t.Fatal("unbalanced markers must fail closed")
	}
}

func TestOmpResetStripsManagedBlock(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	home := t.TempDir()
	a := newOmp(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", AnyPickRoot: root, Home: home,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ompModelsPath(home)); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(ompModelsPath(home))
		t.Fatalf("managed-only file must be removed, got:\n%s", raw)
	}
}

func TestOmpResetKeepsUserContent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	home := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "providers:\n  openrouter:\n    baseUrl: https://openrouter.ai/api/v1\n"
	if err := os.WriteFile(ompModelsPath(home), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", AnyPickRoot: root, Home: home,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	raw := string(mustRead(t, ompModelsPath(home)))
	if !strings.Contains(raw, "openrouter:") || strings.Contains(raw, ompModelsBegin) {
		t.Fatalf("user content lost or block left behind:\n%s", raw)
	}
}

func TestOmpInspectPresenceOnly(t *testing.T) {
	home := t.TempDir()
	a := newOmp(home)
	res, err := a.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if res.Present {
		t.Fatal("empty home must not present")
	}
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(ompAgentDir(home), "config.yml"), []byte("model: test\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	res, err = a.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Present {
		t.Fatal("agent dir must present")
	}
	if len(res.ConfigPaths) != 2 {
		t.Errorf("config paths = %v", res.ConfigPaths)
	}
}

func TestOmpApplyConfigPreservesUserKeysAndComments(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	existingCfg := "# user comment\ntheme: dark\nmodel: custom/old\nmodelRoles:\n  default: custom/old\n"
	if err := os.WriteFile(ompConfigPath(home), []byte(existingCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	_, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk-test",
		DefaultModel: "claude-sonnet-5",
		AnyPickRoot:  root,
		Home:         home,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgRaw := string(mustRead(t, ompConfigPath(home)))
	for _, want := range []string{
		"# user comment",
		"theme: dark",
		"# anypick:pre-proxy: model: custom/old",
		"# anypick:pre-proxy: modelRoles:",
		"# anypick:pre-proxy:   default: custom/old",
		ompConfigBegin,
		"defaultProvider: anypick",
		"defaultModel: 'anypick/claude-sonnet-5'",
		"model: 'anypick/claude-sonnet-5'",
		"modelRoles:",
		"  default: 'anypick/claude-sonnet-5'",
		ompConfigEnd,
	} {
		if !strings.Contains(cfgRaw, want) {
			t.Errorf("config.yml missing %q:\n%s", want, cfgRaw)
		}
	}
}

func TestOmpResetRestoresConfigFromCheckpoint(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	existingCfg := "theme: nord\nmodel: original-model\n"
	if err := os.WriteFile(ompConfigPath(home), []byte(existingCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	t.Setenv("ANYPICK_HOME", root)
	_, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk-test",
		DefaultModel: "new-model",
		AnyPickRoot:  root,
		Home:         home,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	cfgRaw := string(mustRead(t, ompConfigPath(home)))
	if cfgRaw != existingCfg {
		t.Fatalf("config.yml not restored to original; got:\n%s\nwant:\n%s", cfgRaw, existingCfg)
	}
	// Checkpoint must be consumed by Reset
	checkpointPath := filepath.Join(root, "clients", "omp", ompRestoreFile)
	if _, err := os.Stat(checkpointPath); !os.IsNotExist(err) {
		t.Fatalf("checkpoint file must be deleted by Reset: %s", checkpointPath)
	}
}

func TestOmpRestoreNativeKeepsCheckpoint(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(ompAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	existingCfg := "theme: solarized\nmodel: my-model\n"
	if err := os.WriteFile(ompConfigPath(home), []byte(existingCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newOmp(home)
	t.Setenv("ANYPICK_HOME", root)
	_, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk-test",
		DefaultModel: "new-model",
		AnyPickRoot:  root,
		Home:         home,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreNative(); err != nil {
		t.Fatal(err)
	}
	cfgRaw := string(mustRead(t, ompConfigPath(home)))
	if cfgRaw != existingCfg {
		t.Fatalf("config.yml not restored by RestoreNative; got:\n%s\nwant:\n%s", cfgRaw, existingCfg)
	}
	// Checkpoint must be kept by RestoreNative
	checkpointPath := filepath.Join(root, "clients", "omp", ompRestoreFile)
	if _, err := os.Stat(checkpointPath); err != nil {
		t.Fatalf("checkpoint file must be preserved by RestoreNative: %v", err)
	}
}
