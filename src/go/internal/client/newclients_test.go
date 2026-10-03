package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestGeminiApplyWritesEnvAndMarker(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	a := newGemini(home)
	res, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk-test",
		DefaultModel: "gemini-2.0-flash",
		AnyPickRoot:  root,
		Home:         home,
	})
	if err != nil {
		t.Fatal(err)
	}
	envRaw, err := os.ReadFile(filepath.Join(home, ".gemini", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	envText := string(envRaw)
	for _, want := range []string{"GEMINI_API_KEY=sk-test", "GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:9000", "GEMINI_MODEL=gemini-2.0-flash"} {
		if !strings.Contains(envText, want) {
			t.Fatalf(".env missing %q:\n%s", want, envText)
		}
	}
	settingsRaw, err := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(settingsRaw, &doc) != nil || doc[anypickManagedKey] == nil {
		t.Fatalf("settings.json missing managed marker: %s", settingsRaw)
	}
	foundEnvSh := false
	for _, p := range res.ManagedPaths {
		if strings.HasSuffix(p, "env.sh") {
			foundEnvSh = true
		}
	}
	if !foundEnvSh {
		t.Fatalf("env.sh missing from managed paths: %v", res.ManagedPaths)
	}

	// Reset strips managed keys and marker.
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	envAfter := string(mustRead(t, filepath.Join(home, ".gemini", ".env")))
	if strings.Contains(envAfter, "GEMINI_API_KEY") {
		t.Fatalf("reset left GEMINI_API_KEY behind:\n%s", envAfter)
	}
}

func TestGeminiApplyRefusesMalformedSettings(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newGemini(home)
	_, err := a.Apply(&ApplyContext{Endpoint: "https://x", APIKey: "k", AnyPickRoot: t.TempDir(), Home: home})
	if err == nil {
		t.Fatal("want refusal for malformed settings")
	}
	var ae *anyerr.Error
	if !asAnyPickGo(err, &ae) || ae.Code != "CLIENT_CONFIG_INVALID" {
		t.Fatalf("wrong error: %v", err)
	}
}

func asAnyPickGo(err error, target **anyerr.Error) bool {
	if ae, ok := err.(*anyerr.Error); ok {
		*target = ae
		return true
	}
	return false
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestKiroApplyPersistentAndIsolated(t *testing.T) {
	a := newKiro(t.TempDir())
	root := t.TempDir()
	res, err := a.Apply(&ApplyContext{
		Endpoint:    "http://127.0.0.1:9001",
		APIKey:      "tok",
		AnyPickRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(root, "clients", "kiro", "env.sh")
	text := string(mustRead(t, envPath))
	for _, want := range []string{"OPENAI_BASE_URL=\"http://127.0.0.1:9001\"", "ANTHROPIC_AUTH_TOKEN=\"tok\"", "OPENAI_API_KEY=\"tok\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("kiro env.sh missing %q:\n%s", want, text)
		}
	}
	if len(res.ManagedEnvKeys) == 0 {
		t.Fatal("expected managed env keys")
	}

	// Isolated home never touches the live anypick root.
	iso := t.TempDir()
	isoRes, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9002",
		APIKey:       "iso",
		AnyPickRoot:  root,
		IsolatedHome: iso,
	})
	if err != nil {
		t.Fatal(err)
	}
	dotenvPath := filepath.Join(iso, ".kiro-anypick.env")
	mustRead(t, dotenvPath)
	inTree := false
	for _, p := range isoRes.ManagedPaths {
		if strings.HasPrefix(p, iso) {
			inTree = true
		}
	}
	if !inTree {
		t.Fatalf("isolated managed paths escape the isolated tree: %v", isoRes.ManagedPaths)
	}
}

func TestOpenCodeApplyResetRoundTrip(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("ANYPICK_HOME", root)
	a := newOpenCode(home)

	ctx := &ApplyContext{
		Endpoint:     "http://127.0.0.1:9100/v1/",
		APIKey:       "secret-key",
		DefaultModel: "m1",
		ModelRoles:   map[string]string{"default": "m1"},
		AnyPickRoot:  root,
		Home:         home,
	}
	if _, err := a.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	docRaw := mustRead(t, configPath)
	var doc map[string]interface{}
	if json.Unmarshal(docRaw, &doc) != nil {
		t.Fatalf("config not JSON: %s", docRaw)
	}
	if doc["model"] != "anypick-managed/m1" {
		t.Fatalf("model = %v", doc["model"])
	}
	provider := doc["provider"].(map[string]interface{})
	entryRaw, _ := json.Marshal(provider["anypick-managed"])
	var entry struct {
		Npm     string `json:"npm"`
		Options struct {
			BaseURL string `json:"baseURL"`
			APIKey  string `json:"apiKey"`
		} `json:"options"`
	}
	if json.Unmarshal(entryRaw, &entry) != nil {
		t.Fatalf("bad provider entry: %s", entryRaw)
	}
	if entry.Options.BaseURL != "http://127.0.0.1:9100/v1" {
		t.Fatalf("baseURL = %q", entry.Options.BaseURL)
	}
	if entry.Options.APIKey != "{file:"+filepath.Join(home, ".config", "opencode", ".anypick-api-key")+"}" {
		t.Fatalf("apiKey = %q (leaks secret?)", entry.Options.APIKey)
	}
	// Restore state was captured before first write.
	mustRead(t, filepath.Join(root, "clients", "opencode", "restore.json"))

	// A user value for the managed provider is restored on reset.
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	doc2Raw := mustRead(t, configPath)
	var doc2 map[string]interface{}
	if json.Unmarshal(doc2Raw, &doc2) != nil {
		t.Fatalf("post-reset config invalid")
	}
	if _, has := doc2["model"]; has {
		t.Fatalf("model survived reset: %v", doc2["model"])
	}
}

func TestOpenCodeApplyRequiresModelAndEndpoint(t *testing.T) {
	a := newOpenCode(t.TempDir())
	if _, err := a.Apply(&ApplyContext{AnyPickRoot: t.TempDir(), Home: t.TempDir()}); err == nil {
		t.Fatal("expected CLIENT_CONFIG_INVALID without endpoint/model")
	}
}

func TestStripJsoncPreservesStrings(t *testing.T) {
	src := `{
		// comment
		"url": "http://x/not/*a*/comment",
		"trailing": [1, 2,],
	}`
	out := string(stripJsonc([]byte(src)))
	var v map[string]interface{}
	if json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("stripJsonc output unparseable: %s", out)
	}
	if v["url"] != "http://x/not/*a*/comment" {
		t.Fatalf("string contents mutated: %v", v["url"])
	}
}
