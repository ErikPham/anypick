package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPiApplyWritesManagedProviderAndSelection(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newPi(home)

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
	modelsRaw := mustRead(t, piModelsPath(home))
	var modelsDoc map[string]json.RawMessage
	if err := json.Unmarshal(modelsRaw, &modelsDoc); err != nil {
		t.Fatal(err)
	}
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(modelsDoc["providers"], &providers); err != nil {
		t.Fatalf("providers not an object: %s", modelsRaw)
	}
	var entry struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		API     string `json:"api"`
		Models  []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(providers["anypick"], &entry); err != nil {
		t.Fatalf("anypick entry: %v\n%s", err, providers["anypick"])
	}
	if entry.BaseURL != "http://127.0.0.1:9000/v1" {
		t.Errorf("baseUrl = %q", entry.BaseURL)
	}
	if entry.APIKey != "sk-test" || entry.API != "openai-completions" {
		t.Errorf("auth/api = %+v", entry)
	}
	if len(entry.Models) != 2 || entry.Models[0].ID != "gpt-5.6" {
		t.Errorf("models = %+v", entry.Models)
	}
	settingsRaw := mustRead(t, piSettingsPath(home))
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		t.Fatal(err)
	}
	var provider, model string
	_ = json.Unmarshal(settings["defaultProvider"], &provider)
	_ = json.Unmarshal(settings["defaultModel"], &model)
	if provider != "anypick" || model != "anypick/gpt-5.6" {
		t.Errorf("selection = %q/%q", provider, model)
	}
	if len(res.ManagedPaths) != 2 {
		t.Errorf("managed paths = %v", res.ManagedPaths)
	}
}

func TestPiApplyRequiresEndpointAndModel(t *testing.T) {
	home := t.TempDir()
	a := newPi(home)
	if _, err := a.Apply(&ApplyContext{
		APIKey: "sk-test", DefaultModel: "m", AnyPickRoot: t.TempDir(), Home: home,
	}); err == nil {
		t.Fatal("missing endpoint must fail")
	}
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk-test", AnyPickRoot: t.TempDir(), Home: home,
	}); err == nil {
		t.Fatal("missing model must fail")
	}
}

func TestPiApplyRefusesMalformedModels(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(piAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(piModelsPath(home), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newPi(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", DefaultModel: "m",
		AnyPickRoot: t.TempDir(), Home: home,
	}); err == nil {
		t.Fatal("malformed models.json must fail closed, never be replaced")
	}
}

func TestPiResetRestoresCheckpoint(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	a := newPi(home)
	if err := os.MkdirAll(piAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"providers": {}, "theme": "dark"}`
	if err := os.WriteFile(piSettingsPath(home), []byte(`{"defaultProvider": "openrouter", "defaultModel": "openrouter/auto", "theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = before
	if _, err := a.Apply(&ApplyContext{
		Endpoint: "http://127.0.0.1:9000", APIKey: "sk", DefaultModel: "m",
		ModelCatalog: []string{"m"}, AnyPickRoot: root, Home: home,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	settingsRaw := mustRead(t, piSettingsPath(home))
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		t.Fatal(err)
	}
	var provider, model, theme string
	_ = json.Unmarshal(settings["defaultProvider"], &provider)
	_ = json.Unmarshal(settings["defaultModel"], &model)
	_ = json.Unmarshal(settings["theme"], &theme)
	if provider != "openrouter" || model != "openrouter/auto" || theme != "dark" {
		t.Errorf("checkpoint not restored: %s", settingsRaw)
	}
	modelsRaw := mustRead(t, piModelsPath(home))
	if strings.Contains(string(modelsRaw), piManagedProviderID) {
		t.Fatalf("managed entry left behind:\n%s", modelsRaw)
	}
}

func TestPiInspectReadsAuthKeysWithoutValues(t *testing.T) {
	home := t.TempDir()
	a := newPi(home)
	res, err := a.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if res.Present {
		t.Fatal("empty home must not present")
	}
	if err := os.MkdirAll(piAgentDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	auth := `{"openai": {"type": "api_key", "key": "sk-secret-value"}, "openrouter": {"type": "api_key", "key": "sk-or-secret"}}`
	if err := os.WriteFile(piAuthPath(home), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = a.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Present {
		t.Fatal("agent dir with auth must present")
	}
	if !strings.Contains(res.Summary, "2 provider keys") {
		t.Errorf("summary = %q", res.Summary)
	}
	if strings.Contains(res.Summary, "sk-secret-value") || strings.Contains(res.Summary, "sk-or-secret") {
		t.Fatalf("key material crossed into summary: %q", res.Summary)
	}
	if len(res.ConfigPaths) != 2 {
		t.Errorf("config paths = %v", res.ConfigPaths)
	}
}
