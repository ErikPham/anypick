package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

// clearWorkBuddyEnv makes path resolution hermetic. Both product variables
// can select the runtime's models.json root.
func clearWorkBuddyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
}

// workbuddyPaths resolves the target files with the ambient overrides cleared.
// Every test must go through this rather than workbuddyModelsPaths directly:
// resolving first and clearing later can write the fixture into the
// developer's real config directory.
func workbuddyPaths(t *testing.T, home string) []string {
	t.Helper()
	clearWorkBuddyEnv(t)
	return workbuddyModelsPaths(home)
}

func workbuddyApplyContext(t *testing.T, home string) *ApplyContext {
	t.Helper()
	clearWorkBuddyEnv(t)
	root := filepath.Join(home, "anypick")
	// Reset() resolves its checkpoint through anyPickRootFallback(), which
	// reads ANYPICK_HOME — point it at the root Apply writes to so the two
	// agree inside a test.
	t.Setenv("ANYPICK_HOME", root)
	return &ApplyContext{
		Endpoint:     "http://127.0.0.1:8787",
		APIKey:       "proxy-secret",
		DefaultModel: "gpt-5",
		AnyPickRoot:  root,
		Home:         home,
	}
}

func readWorkBuddyFile(t *testing.T, path string) map[string]json.RawMessage {
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

func TestWorkBuddyClientCapabilities(t *testing.T) {
	a := newWorkBuddy(t.TempDir())
	if a.ID() != "workbuddy" || a.Name() != "WorkBuddy AI" {
		t.Fatalf("id/name = %q/%q", a.ID(), a.Name())
	}
	caps := a.Capabilities()
	if caps.ID != "workbuddy" || caps.ProtocolPreference != transport.OpenAI {
		t.Fatalf("capabilities = %#v", caps)
	}
	if !caps.SupportsPersistentConfig {
		t.Fatal("models.json is a persistent config write")
	}
}

func TestWorkBuddyApplyWritesManagedEntry(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)

	res, err := a.Apply(ctx)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	path := workbuddyPaths(t, home)[0]
	if len(res.ManagedPaths) == 0 || res.ManagedPaths[0] != path {
		t.Fatalf("managed paths = %v, want %s first", res.ManagedPaths, path)
	}

	entries := workbuddyEntries(readWorkBuddyFile(t, path))
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	e := entries[0]
	if e.ID != "custom-local:gpt-5" || e.Vendor != workbuddyVendorID || e.APIKey != "proxy-secret" {
		t.Fatalf("entry = %+v", e)
	}
	// CodeBuddy expects the complete chat-completions path.
	if e.URL != "http://127.0.0.1:8787/v1/chat/completions" {
		t.Fatalf("entry url = %q", e.URL)
	}
	if e.MaxInputTokens != workbuddyDefaultMaxInputTokens || e.MaxOutputTokens != workbuddyDefaultMaxOutputTokens {
		t.Fatalf("token limits = %d/%d", e.MaxInputTokens, e.MaxOutputTokens)
	}
	if e.SupportsToolCall == nil || !*e.SupportsToolCall ||
		e.SupportsImages == nil || !*e.SupportsImages ||
		e.SupportsReasoning == nil || !*e.SupportsReasoning {
		t.Fatalf("capability flags = tool=%v images=%v reasoning=%v",
			e.SupportsToolCall, e.SupportsImages, e.SupportsReasoning)
	}
}

func TestWorkBuddyApplySkipsRouterAliases(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	ctx.DefaultModel = "fast-model"
	ctx.ModelCatalog = []string{"fast-model", "default-model", "hy3", "balanced-model"}

	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	entries := workbuddyEntries(readWorkBuddyFile(t, workbuddyPaths(t, home)[0]))
	if len(entries) != 1 || entries[0].ID != "custom-local:hy3" {
		t.Fatalf("entries = %+v, want only hy3", entries)
	}
}

func TestWorkBuddyApplyRejectsOnlyRouterModels(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	ctx.DefaultModel = "fast-model"
	ctx.ModelCatalog = []string{"fast-model", "default-model"}
	if _, err := a.Apply(ctx); err == nil {
		t.Fatal("Apply of only WorkBuddy router aliases should fail")
	}
}

func TestWorkBuddyApplyShowsSelectedAccountInManagedModelName(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	ctx.SourceName = "outof0"

	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	entries := workbuddyEntries(readWorkBuddyFile(t, workbuddyPaths(t, home)[0]))
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	if entries[0].Name != "AnyPick · outof0 · gpt-5" {
		t.Fatalf("managed model name = %q", entries[0].Name)
	}
}

// TestWorkBuddyApplyIsIdempotent guards the merge: models.json is a file the
// user keeps editing, so re-running must not duplicate our entry or drop theirs.
func TestWorkBuddyApplyIsIdempotentAndPreservesUserEntries(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	path := workbuddyPaths(t, home)[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{
  "models": [
    {"id": "mine", "name": "Mine", "vendor": "someone", "apiKey": "k", "url": "https://example.com/v1", "note": "keep me"}
  ],
  "unrelated": 42
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, err := a.Apply(ctx); err != nil {
			t.Fatalf("Apply #%d: %v", i, err)
		}
	}

	doc := readWorkBuddyFile(t, path)
	if _, ok := doc["unrelated"]; !ok {
		t.Fatal("unrelated top-level key dropped")
	}
	entries := workbuddyEntries(doc)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the user's plus one of ours", entries)
	}
	var user, managed *workbuddyModelEntry
	for i := range entries {
		switch entries[i].Vendor {
		case workbuddyVendorID:
			managed = &entries[i]
		default:
			user = &entries[i]
		}
	}
	if managed == nil || user == nil {
		t.Fatalf("expected one managed and one user entry: %+v", entries)
	}
	// A field AnyPick does not model must survive the round trip.
	if _, ok := user.raw["note"]; !ok {
		t.Fatal("unknown user field dropped")
	}
}

func TestWorkBuddyApplyRefusesUnreadableFile(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	path := workbuddyPaths(t, home)[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(workbuddyApplyContext(t, home)); err == nil {
		t.Fatal("Apply should refuse to overwrite an unreadable models.json")
	}
}

// The WorkBuddy app's own model sync can leave a bare `[]` behind. That is an
// empty list, not user data: Apply must repair it instead of refusing forever.
func TestWorkBuddyApplyRepairsEmptyArrayFile(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	path := workbuddyPaths(t, home)[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(workbuddyApplyContext(t, home)); err != nil {
		t.Fatalf("Apply over an empty array: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Models) == 0 {
		t.Fatalf("models.json was not repaired: %v\n%s", err, raw)
	}
}

func TestWorkBuddyApplyRequiresEndpointAndModel(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	ctx.Endpoint = ""
	if _, err := a.Apply(ctx); err == nil {
		t.Fatal("Apply without an endpoint should fail")
	}
	ctx = workbuddyApplyContext(t, home)
	ctx.DefaultModel = ""
	if _, err := a.Apply(ctx); err == nil {
		t.Fatal("Apply without a model should fail")
	}
}

func TestWorkBuddyApplyDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	ctx.DryRun = true
	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply dry-run: %v", err)
	}
	for _, path := range workbuddyPaths(t, home) {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("dry-run wrote %s", path)
		}
	}
}

func TestWorkBuddyResetRestoresUserList(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	ctx := workbuddyApplyContext(t, home)
	path := workbuddyPaths(t, home)[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"models":[{"id":"mine","name":"Mine","vendor":"someone","url":"https://example.com/v1"}]}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if entries := workbuddyEntries(readWorkBuddyFile(t, path)); len(entries) != 2 {
		t.Fatalf("after Apply entries = %+v, want 2", entries)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	entries := workbuddyEntries(readWorkBuddyFile(t, path))
	if len(entries) != 1 || entries[0].ID != "mine" || entries[0].Vendor != "someone" {
		t.Fatalf("after Reset entries = %+v, want only the user's", entries)
	}
}

// TestWorkBuddyResetRemovesFilesItCreated covers the other half of the
// checkpoint: when AnyPick created models.json itself, Reset must delete it
// rather than leave an empty object behind.
func TestWorkBuddyResetRemovesFilesItCreated(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	if _, err := a.Apply(workbuddyApplyContext(t, home)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	path := workbuddyPaths(t, home)[0]
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Apply did not create %s: %v", path, err)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("Reset left %s behind", path)
	}
}

func TestWorkBuddyResetWithoutCheckpointStripsManagedOnly(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	// Point Reset at a root with no checkpoint, so it must fall back to
	// recognising AnyPick's own entries instead.
	t.Setenv("ANYPICK_HOME", filepath.Join(home, "no-checkpoint"))
	path := workbuddyPaths(t, home)[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// Simulate an AnyPick-managed file whose checkpoint is lost.
	seeded := `{"models":[{"id":"mine","vendor":"someone"},{"id":"borrowed","vendor":"anypick"}]}`
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	entries := workbuddyEntries(readWorkBuddyFile(t, path))
	if len(entries) != 1 || entries[0].ID != "mine" {
		t.Fatalf("entries after Reset = %+v, want only mine", entries)
	}
}

func TestWorkBuddyInspect(t *testing.T) {
	home := t.TempDir()
	a := newWorkBuddy(home)
	clearWorkBuddyEnv(t)

	res, err := a.Inspect()
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if res.Present {
		t.Fatalf("Inspect = %+v, want absent with no models.json", res)
	}

	if _, err := a.Apply(workbuddyApplyContext(t, home)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	res, err = a.Inspect()
	if err != nil {
		t.Fatalf("Inspect after Apply: %v", err)
	}
	if !res.Present || res.Summary == "" {
		t.Fatalf("Inspect after Apply = %+v", res)
	}
	if len(res.ConfigPaths) == 0 || res.ConfigPaths[0] != workbuddyPaths(t, home)[0] {
		t.Fatalf("Inspect config paths = %v", res.ConfigPaths)
	}
}

// TestWorkBuddyModelsPathsMatchesCodeBuddyRuntime pins both supported runtime
// roots and the explicit environment overrides used by desktop and CLI.
func TestWorkBuddyModelsPathsMatchesCodeBuddyRuntime(t *testing.T) {
	home := t.TempDir()
	clearWorkBuddyEnv(t)

	// WorkBuddy desktop is the installed product and uses this root by default.
	if got := workbuddyPaths(t, home); len(got) != 1 ||
		got[0] != filepath.Join(home, ".workbuddy-ai", "models.json") {
		t.Fatalf("default paths = %v", got)
	}

	if err := os.MkdirAll(filepath.Join(home, ".workbuddy-ai"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codebuddy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := workbuddyPaths(t, home); len(got) != 2 ||
		got[0] != filepath.Join(home, ".workbuddy-ai", "models.json") ||
		got[1] != filepath.Join(home, ".codebuddy", "models.json") {
		t.Fatalf("paths with both product dirs = %v", got)
	}

	// Either product can explicitly select one root; CodeBuddy wins when both
	// variables are present because that is the runtime's model resolver.
	t.Setenv("WORKBUDDY_CONFIG_DIR", "/tmp/wb-override")
	if got := workbuddyModelsPaths(home); len(got) != 1 ||
		got[0] != filepath.Join("/tmp/wb-override", "models.json") {
		t.Fatalf("desktop override paths = %v", got)
	}

	t.Setenv("CODEBUDDY_CONFIG_DIR", "/tmp/cb-override")
	if got := workbuddyModelsPaths(home); len(got) != 1 ||
		got[0] != filepath.Join("/tmp/cb-override", "models.json") {
		t.Fatalf("secondary override paths = %v", got)
	}
}
