package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestDshApplyWritesEnvAndPatch(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	a := newDsh(home)

	res, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk-test",
		DefaultModel: "deepseek-chat",
		AnyPickRoot:  root,
		Home:         home,
	})
	if err != nil {
		t.Fatal(err)
	}

	// .env carries the proxy key + base URL (dsh reads it as environment layers).
	envRaw := mustRead(t, dshEnvPath(home))
	envText := string(envRaw)
	for _, want := range []string{
		"DEEPSEEK_API_KEY=sk-test",
		"DEEPSEEK_BASE_URL=http://127.0.0.1:9000/v1",
	} {
		if !strings.Contains(envText, want) {
			t.Fatalf(".env missing %q:\n%s", want, envText)
		}
	}

	// cordis patch pins the default model to the chosen account model.
	patchRaw := mustRead(t, dshPatchPath(home))
	patchText := string(patchRaw)
	for _, want := range []string{
		dshPatchBegin,
		"name: '" + dshDefaultModelPlugin + "'",
		"provider: " + dshProviderRoute,
		"model: deepseek-chat",
		dshPatchEnd,
	} {
		if !strings.Contains(patchText, want) {
			t.Fatalf("cordis patch missing %q:\n%s", want, patchText)
		}
	}

	// Launcher env files emitted to the AnyPick root for persistent runs.
	foundEnvSh := false
	for _, p := range res.ManagedPaths {
		if strings.HasSuffix(p, "env.sh") {
			foundEnvSh = true
		}
	}
	if !foundEnvSh {
		t.Fatalf("env.sh missing from managed paths: %v", res.ManagedPaths)
	}

	// Reset must strip managed env keys and remove the managed block.
	if err := a.Reset(); err != nil {
		t.Fatal(err)
	}
	envAfter := string(mustRead(t, dshEnvPath(home)))
	if strings.Contains(envAfter, "DEEPSEEK_API_KEY") {
		t.Fatalf("reset left DEEPSEEK_API_KEY behind:\n%s", envAfter)
	}
	if _, err := os.Stat(dshPatchPath(home)); !os.IsNotExist(err) {
		t.Fatalf("reset left cordis patch at %s", dshPatchPath(home))
	}
}

func TestDshApplyRefusesNonSequencePatch(t *testing.T) {
	home := t.TempDir()
	dir := dshHome(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A map document is not a valid cordis patch sequence.
	if err := os.WriteFile(dshPatchPath(home), []byte("foo: bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newDsh(home)
	_, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9000",
		APIKey:       "sk",
		DefaultModel: "deepseek-chat",
		AnyPickRoot:  t.TempDir(),
		Home:         home,
	})
	if err == nil {
		t.Fatal("want refusal for non-sequence cordis patch")
	}
	var ae *anyerr.Error
	if !asAnyPickGo(err, &ae) || ae.Code != "CLIENT_CONFIG_INVALID" {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestDshApplyIsolatedStaysInTree(t *testing.T) {
	a := newDsh(t.TempDir())
	root := t.TempDir()
	iso := t.TempDir()

	isoRes, err := a.Apply(&ApplyContext{
		Endpoint:     "http://127.0.0.1:9100",
		APIKey:       "iso",
		DefaultModel: "deepseek-chat",
		AnyPickRoot:  root,
		IsolatedHome: iso,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Managed paths must live under the isolated tree, never the live root.
	for _, p := range isoRes.ManagedPaths {
		if !strings.HasPrefix(p, iso) {
			t.Fatalf("isolated managed path escapes tree: %s", p)
		}
	}
	// The isolated harness home actually got the env + patch.
	if _, err := os.Stat(filepath.Join(iso, ".dsh", ".env")); err != nil {
		t.Fatalf("isolated .env not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(iso, ".dsh", "cordis.patch.yml")); err != nil {
		t.Fatalf("isolated cordis patch not written: %v", err)
	}
}
