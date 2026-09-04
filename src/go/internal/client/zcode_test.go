package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

// seedZCodeDirs creates the config directories AnyPick keys off. Nothing else
// in these tests may create ~/.zcode by accident, so every fixture is explicit.
// seedZCodeDirs provisions a flavour the way ZCode leaves it once it has been
// configured: the directory *and* a config.json inside it.
func seedZCodeDirs(t *testing.T, home string, flavours ...string) []string {
	t.Helper()
	paths := make([]string, 0, len(flavours))
	for _, f := range flavours {
		dir := filepath.Join(home, ".zcode", f)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{"provider":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// TestZCodeIgnoresDirectoriesWithoutAConfig keeps target selection keyed on the
// config file rather than the directory. ~/.zcode/cli exists on machines that
// have never written a CLI config — it holds db, log and exec trees — and
// creating a config.json there would shadow the builtin providers the CLI
// would otherwise fall back to.
func TestZCodeIgnoresDirectoriesWithoutAConfig(t *testing.T) {
	home := t.TempDir()
	v2 := seedZCodeDirs(t, home, "v2")[0]
	if err := os.MkdirAll(filepath.Join(home, ".zcode", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}

	got := ZCodeConfigPaths(home)
	if len(got) != 1 || got[0] != v2 {
		t.Fatalf("targets = %v, want only %s", got, v2)
	}

	a := newZCode(home)
	if _, err := a.Apply(zcodeApplyContext(t, home)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zcode", "cli", "config.json")); !os.IsNotExist(err) {
		t.Fatal("Apply created a config in ~/.zcode/cli, which has none of its own")
	}
}

// zcodeApplyContext mirrors workbuddyApplyContext: Reset() resolves its
// checkpoint through anyPickRootFallback(), so ANYPICK_HOME must agree with the
// root Apply writes to.
func zcodeApplyContext(t *testing.T, home string) *ApplyContext {
	t.Helper()
	root := filepath.Join(home, "anypick")
	t.Setenv("ANYPICK_HOME", root)
	return &ApplyContext{
		Endpoint:     "http://127.0.0.1:8787",
		APIKey:       "proxy-secret",
		DefaultModel: "claude-sonnet-4-5",
		AnyPickRoot:  root,
		Home:         home,
	}
}

func readZCodeFile(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func zcodeEntry(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	doc := readZCodeFile(t, path)
	providers, err := zcodeProviders(doc)
	if err != nil {
		t.Fatalf("providers in %s: %v", path, err)
	}
	raw, ok := providers[zcodeManagedProviderID]
	if !ok {
		t.Fatalf("no %q provider in %s: %v", zcodeManagedProviderID, path, providers)
	}
	entry := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("entry in %s: %v", path, err)
	}
	return entry
}

func zcodeString(t *testing.T, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	return stringField(obj, key)
}

func TestZCodeClientCapabilities(t *testing.T) {
	a := newZCode(t.TempDir())
	if a.ID() != "zcode" || a.Name() != "ZCode" {
		t.Fatalf("id/name = %q/%q", a.ID(), a.Name())
	}
	caps := a.Capabilities()
	if caps.ID != "zcode" || caps.ProtocolPreference != transport.Anthropic {
		t.Fatalf("capabilities = %#v", caps)
	}
	if !hasProtocol(caps.AcceptedProtocols, transport.Anthropic) ||
		!hasProtocol(caps.AcceptedProtocols, transport.OpenAI) {
		t.Fatalf("accepted protocols = %v, want both", caps.AcceptedProtocols)
	}
	if !caps.SupportsPersistentConfig {
		t.Fatal("config.json is a persistent config write")
	}
}

// TestZCodeTargetsCoversBothFlavours pins the rule that keeps this from failing
// silently: the desktop app reads ~/.zcode/v2 and the CLI reads ~/.zcode/cli,
// and nothing observable says which one the user will run.
func TestZCodeTargetsCoversBothFlavours(t *testing.T) {
	home := t.TempDir()

	// Nothing installed: target only the CLI file, the documented schema.
	got := ZCodeConfigPaths(home)
	if len(got) != 1 || got[0] != filepath.Join(home, ".zcode", "cli", "config.json") {
		t.Fatalf("default targets = %v", got)
	}

	seedZCodeDirs(t, home, "v2")
	got = ZCodeConfigPaths(home)
	if len(got) != 1 || got[0] != filepath.Join(home, ".zcode", "v2", "config.json") {
		t.Fatalf("targets with v2 only = %v", got)
	}

	seedZCodeDirs(t, home, "cli")
	got = ZCodeConfigPaths(home)
	if len(got) != 2 {
		t.Fatalf("targets with both = %v, want 2", got)
	}
	if got[0] != filepath.Join(home, ".zcode", "cli", "config.json") ||
		got[1] != filepath.Join(home, ".zcode", "v2", "config.json") {
		t.Fatalf("targets order = %v", got)
	}
}

func TestZCodeApplyWritesBothFlavoursInTheirOwnSchema(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "cli", "v2")
	a := newZCode(home)

	res, err := a.Apply(zcodeApplyContext(t, home))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.ManagedPaths) != 3 {
		t.Fatalf("managed paths = %v, want both configs plus the checkpoint", res.ManagedPaths)
	}

	// CLI — schema zcode.config.v1.
	cli := zcodeEntry(t, paths[0])
	if got := zcodeString(t, cli, "id"); got != zcodeManagedProviderID {
		t.Fatalf("cli entry id = %q", got)
	}
	if got := zcodeString(t, cli, "kind"); got != "anthropic" {
		t.Fatalf("cli entry kind = %q", got)
	}
	var cliOptions struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	}
	if err := json.Unmarshal(cli["options"], &cliOptions); err != nil {
		t.Fatalf("cli options: %v", err)
	}
	if cliOptions.BaseURL != "http://127.0.0.1:8787" || cliOptions.APIKey != "proxy-secret" {
		t.Fatalf("cli options = %+v", cliOptions)
	}
	// zcode.config.v1 models carry flat capability flags.
	var cliModels map[string]struct {
		ContextWindow    int  `json:"contextWindow"`
		MaxOutputTokens  int  `json:"maxOutputTokens"`
		SupportsToolCall bool `json:"supportsToolCall"`
	}
	if err := json.Unmarshal(cli["models"], &cliModels); err != nil {
		t.Fatalf("cli models: %v", err)
	}
	m, ok := cliModels["claude-sonnet-4-5"]
	if !ok {
		t.Fatalf("cli models = %+v, want the selected model", cliModels)
	}
	if m.ContextWindow == 0 || m.MaxOutputTokens == 0 || !m.SupportsToolCall {
		t.Fatalf("cli model entry = %+v", m)
	}
	// The CLI has no --model flag: routing is not done until model.main points
	// at the managed provider.
	cliDoc := readZCodeFile(t, paths[0])
	var cliModel struct {
		Main string `json:"main"`
		Lite string `json:"lite"`
	}
	if err := json.Unmarshal(cliDoc["model"], &cliModel); err != nil {
		t.Fatalf("cli model: %v", err)
	}
	if cliModel.Main != "anypick/claude-sonnet-4-5" {
		t.Fatalf("cli model.main = %q", cliModel.Main)
	}

	// Desktop v2 — nested limits, and enabled/source so the picker offers it.
	v2 := zcodeEntry(t, paths[1])
	if _, ok := v2["id"]; ok {
		t.Fatalf("v2 entry must not carry the CLI-only id field: %v", v2)
	}
	var v2Options struct {
		BaseURL string `json:"baseURL"`
	}
	if err := json.Unmarshal(v2["options"], &v2Options); err != nil {
		t.Fatalf("v2 options: %v", err)
	}
	if v2Options.BaseURL != "http://127.0.0.1:8787" {
		t.Fatalf("v2 baseURL = %q", v2Options.BaseURL)
	}
	var v2Entry struct {
		Enabled bool   `json:"enabled"`
		Source  string `json:"source"`
		Models  map[string]struct {
			Limit struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	raw, _ := json.Marshal(v2)
	if err := json.Unmarshal(raw, &v2Entry); err != nil {
		t.Fatalf("v2 entry: %v", err)
	}
	if !v2Entry.Enabled || v2Entry.Source != "custom" {
		t.Fatalf("v2 enabled/source = %v/%q", v2Entry.Enabled, v2Entry.Source)
	}
	vm, ok := v2Entry.Models["claude-sonnet-4-5"]
	if !ok || vm.Limit.Context == 0 || vm.Limit.Output == 0 {
		t.Fatalf("v2 models = %+v", v2Entry.Models)
	}
	// The desktop app keeps its selection in setting.json, which AnyPick does
	// not own: it must not invent a top-level model key there.
	if _, ok := readZCodeFile(t, paths[1])["model"]; ok {
		t.Fatal("v2 config must not gain a top-level model key")
	}
}

// TestZCodeApplyIsIdempotentAndPreservesUserProviders guards the merge: the
// user keeps adding providers in ZCode's own UI, so a re-run must neither
// duplicate our entry nor drop theirs.
func TestZCodeApplyIsIdempotentAndPreservesUserProviders(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "cli", "v2")
	a := newZCode(home)
	for _, p := range paths {
		original := `{
  "provider": {
    "deepseek": {"id": "deepseek", "kind": "anthropic", "name": "DeepSeek",
                 "options": {"baseURL": "https://api.deepseek.com/anthropic"}}
  },
  "unrelated": 42
}`
		if err := os.WriteFile(p, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 2; i++ {
		if _, err := a.Apply(zcodeApplyContext(t, home)); err != nil {
			t.Fatalf("Apply #%d: %v", i, err)
		}
	}

	for _, p := range paths {
		doc := readZCodeFile(t, p)
		if _, ok := doc["unrelated"]; !ok {
			t.Fatalf("%s: unrelated top-level key dropped", p)
		}
		providers, err := zcodeProviders(doc)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(providers) != 2 {
			t.Fatalf("%s: providers = %v, want the user's plus one of ours", p, providers)
		}
		if _, ok := providers["deepseek"]; !ok {
			t.Fatalf("%s: user provider dropped", p)
		}
	}
}

func TestZCodeApplyTrimsTrailingSlash(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "cli")
	a := newZCode(home)
	ctx := zcodeApplyContext(t, home)
	ctx.Endpoint = "http://127.0.0.1:8787/"
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	entry := zcodeEntry(t, paths[0])
	var options struct {
		BaseURL string `json:"baseURL"`
	}
	if err := json.Unmarshal(entry["options"], &options); err != nil {
		t.Fatalf("options: %v", err)
	}
	// ZCode joins the base URL with /v1/messages; a trailing slash would
	// produce a double slash.
	if options.BaseURL != "http://127.0.0.1:8787" {
		t.Fatalf("baseURL = %q", options.BaseURL)
	}
}

func TestZCodeApplyRefusesUnreadableFile(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "v2")
	a := newZCode(home)
	if err := os.WriteFile(paths[0], []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(zcodeApplyContext(t, home)); err == nil {
		t.Fatal("Apply should refuse to overwrite an unreadable config.json")
	}
}

func TestZCodeApplyRequiresEndpointAndModel(t *testing.T) {
	home := t.TempDir()
	seedZCodeDirs(t, home, "cli")
	a := newZCode(home)

	ctx := zcodeApplyContext(t, home)
	ctx.Endpoint = ""
	if _, err := a.Apply(ctx); err == nil {
		t.Fatal("Apply without an endpoint should fail")
	}
	ctx = zcodeApplyContext(t, home)
	ctx.DefaultModel = ""
	if _, err := a.Apply(ctx); err == nil {
		t.Fatal("Apply without a model should fail")
	}
}

func TestZCodeApplyDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "cli", "v2")
	a := newZCode(home)
	ctx := zcodeApplyContext(t, home)
	ctx.DryRun = true
	res, err := a.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply dry-run: %v", err)
	}
	if len(res.ManagedPaths) != 3 {
		t.Fatalf("dry-run managed paths = %v", res.ManagedPaths)
	}
	for _, p := range paths {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if string(got) != `{"provider":{}}` {
			t.Fatalf("dry-run modified %s:\n%s", p, got)
		}
	}
}

func TestZCodeResetRestoresUserProviderAndModel(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "cli", "v2")
	a := newZCode(home)
	original := `{
  "provider": {
    "deepseek": {"id": "deepseek", "name": "DeepSeek"}
  },
  "model": {"main": "deepseek/deepseek-chat", "lite": "deepseek/deepseek-chat"}
}`
	for _, p := range paths {
		if err := os.WriteFile(p, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.Apply(zcodeApplyContext(t, home)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Only the CLI flavour carries a top-level model selection: the desktop app
	// keeps its selection in setting.json, which AnyPick does not own.
	cliDoc := readZCodeFile(t, paths[0])
	var sel struct {
		Main string `json:"main"`
	}
	if err := json.Unmarshal(cliDoc["model"], &sel); err != nil {
		t.Fatalf("%s: model: %v", paths[0], err)
	}
	if sel.Main != "anypick/claude-sonnet-4-5" {
		t.Fatalf("%s: model.main after Apply = %q", paths[0], sel.Main)
	}

	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, p := range paths {
		doc := readZCodeFile(t, p)
		providers, err := zcodeProviders(doc)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if len(providers) != 1 {
			t.Fatalf("%s: providers after Reset = %v, want only the user's", p, providers)
		}
		if _, ok := providers["deepseek"]; !ok {
			t.Fatalf("%s: user provider not restored", p)
		}
		var sel struct {
			Main string `json:"main"`
		}
		if err := json.Unmarshal(doc["model"], &sel); err != nil {
			t.Fatalf("%s: model after Reset: %v", p, err)
		}
		if sel.Main != "deepseek/deepseek-chat" {
			t.Fatalf("%s: model.main after Reset = %q", p, sel.Main)
		}
	}
}

// TestZCodeResetRemovesFilesItCreated covers the other half of the checkpoint.
// It deliberately provisions nothing: Apply creates the config only through the
// "no flavour installed yet" fallback, which is the one path that invents a
// file, so it is the one that must take it away again.
func TestZCodeResetRemovesFilesItCreated(t *testing.T) {
	home := t.TempDir()
	a := newZCode(home)
	paths := ZCodeConfigPaths(home)
	if _, err := a.Apply(zcodeApplyContext(t, home)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("Apply did not create %s: %v", p, err)
		}
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("Reset left %s behind", p)
		}
	}
}

func TestZCodeResetWithoutCheckpointStripsManagedOnly(t *testing.T) {
	home := t.TempDir()
	paths := seedZCodeDirs(t, home, "cli", "v2")
	a := newZCode(home)
	// Point Reset at a root with no checkpoint so it must fall back to
	// recognising AnyPick's own entry instead.
	t.Setenv("ANYPICK_HOME", filepath.Join(home, "no-checkpoint"))
	seeded := `{
  "provider": {
    "deepseek": {"id": "deepseek", "name": "DeepSeek"},
    "anypick": {"id": "anypick", "kind": "anthropic", "name": "AnyPick"}
  },
  "model": {"main": "anypick/claude-sonnet-4-5", "lite": "anypick/claude-sonnet-4-5"}
}`
	for _, p := range paths {
		if err := os.WriteFile(p, []byte(seeded), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, p := range paths {
		doc := readZCodeFile(t, p)
		providers, err := zcodeProviders(doc)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if _, ok := providers["anypick"]; ok {
			t.Fatalf("%s: managed provider survived Reset", p)
		}
		if _, ok := providers["deepseek"]; !ok {
			t.Fatalf("%s: user provider dropped", p)
		}
		if _, ok := doc["model"]; ok {
			t.Fatalf("%s: model still points at the managed provider", p)
		}
	}
}

func TestZCodeInspect(t *testing.T) {
	home := t.TempDir()
	a := newZCode(home)

	// Directories alone are not a config — see TestZCodeIgnoresDirectoriesWithoutAConfig.
	for _, f := range []string{"cli", "v2"} {
		if err := os.MkdirAll(filepath.Join(home, ".zcode", f), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.Inspect()
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if res.Present {
		t.Fatalf("Inspect = %+v, want absent with no config.json", res)
	}

	paths := seedZCodeDirs(t, home, "cli", "v2")
	if _, err := a.Apply(zcodeApplyContext(t, home)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	res, err = a.Inspect()
	if err != nil {
		t.Fatalf("Inspect after Apply: %v", err)
	}
	if !res.Present || res.Summary == "" {
		t.Fatalf("Inspect after Apply = %+v", res)
	}
	if len(res.ConfigPaths) != 2 || res.ConfigPaths[0] != paths[0] {
		t.Fatalf("Inspect config paths = %v", res.ConfigPaths)
	}
}
