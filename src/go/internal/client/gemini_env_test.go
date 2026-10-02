package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeminiDirFollowsTheVariableTheCLIActuallyReads pins the resolution order
// to what @google/gemini-cli 0.59.0 really reads. Grep the published bundle:
// GEMINI_CONFIG_DIR has zero hits, GEMINI_CLI_HOME is the one that exists and
// it replaces HOME rather than naming the config dir.
func TestGeminiDirFollowsTheVariableTheCLIActuallyReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// GEMINI_CLI_HOME replaces HOME, so .gemini is joined onto it.
	t.Setenv("GEMINI_CLI_HOME", "/tmp/anypick-cli-home")
	t.Setenv("GEMINI_CONFIG_DIR", "")
	want := filepath.Join("/tmp/anypick-cli-home", ".gemini")
	if got := geminiDir(home); got != want {
		t.Fatalf("geminiDir = %q, want %q", got, want)
	}

	// With both set the CLI follows GEMINI_CLI_HOME, so AnyPick must too —
	// otherwise it writes where the CLI never looks, which in an isolated
	// home means writing into the sandbox while the CLI reads the real
	// ~/.gemini.
	t.Setenv("GEMINI_CONFIG_DIR", "/tmp/anypick-legacy-config")
	if got := geminiDir(home); got != want {
		t.Fatalf("GEMINI_CONFIG_DIR beat GEMINI_CLI_HOME: %q", got)
	}

	// AnyPick's own knob still applies when the CLI's is absent.
	t.Setenv("GEMINI_CLI_HOME", "")
	if got := geminiDir(home); got != "/tmp/anypick-legacy-config" {
		t.Fatalf("geminiDir = %q, want /tmp/anypick-legacy-config", got)
	}
}

func TestGeminiApplyDropsTheVariableTheCLINeverReads(t *testing.T) {
	home := t.TempDir()
	a := newGemini(home)
	if _, err := a.Apply(&ApplyContext{
		Endpoint:    "http://127.0.0.1:9000",
		APIKey:      "sk-test",
		AnyPickRoot: t.TempDir(),
		Home:        home,
	}); err != nil {
		t.Fatal(err)
	}
	envText := string(mustRead(t, filepath.Join(home, ".gemini", ".env")))
	if !strings.Contains(envText, "GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:9000") {
		t.Fatalf("GOOGLE_GEMINI_BASE_URL missing:\n%s", envText)
	}
	// GEMINI_API_BASE_URL belongs to the Google GenAI SDK, not to the CLI:
	// zero hits in the @google/gemini-cli 0.59.0 bundle.
	if strings.Contains(envText, "GEMINI_API_BASE_URL") {
		t.Fatalf("wrote GEMINI_API_BASE_URL, which the CLI never reads:\n%s", envText)
	}
}

// TestGeminiInspectCountsARestoredCredential covers the direct path: it
// restores an OAuth file and writes no .env and no settings.json, so counting
// only those two files made every direct gemini activation report "no
// anypick-managed configuration after activation" while the login had in fact
// been restored.
func TestGeminiInspectCountsARestoredCredential(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a := newGemini(home)

	got, err := a.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if got.Present {
		t.Fatal("an empty home must not report a configuration")
	}

	// Gemini CLI and Antigravity are one identity: whichever OAuth file the
	// login left behind counts, regardless of which app wrote it.
	for _, name := range geminiCredentialFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := a.Inspect()
		if err != nil {
			t.Fatal(err)
		}
		if !got.Present {
			t.Fatalf("%s on disk but Inspect reports absent", name)
		}
		if got.Summary != "restored "+name {
			t.Fatalf("summary = %q, want %q", got.Summary, "restored "+name)
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGeminiRestoreNativeStripsProxyKeys covers the unroute half of a proxy
// binding: .env carries the loopback endpoint and the proxy token, and the
// managed marker lists exactly those keys. Restoration must remove them —
// otherwise the CLI keeps talking to a stopped proxy and DetectLive reports
// "API key" for a proxy token.
func TestGeminiRestoreNativeStripsProxyKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("ANYPICK_HOME", t.TempDir())
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("GEMINI_API_KEY=proxy-token\nGOOGLE_GEMINI_BASE_URL=http://127.0.0.1:9\nKEEP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"theme":"dark","_anypickManaged":{"keys":["GEMINI_API_KEY","GOOGLE_GEMINI_BASE_URL"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a := newGemini(home)
	if err := a.RestoreNative(); err != nil {
		t.Fatalf("RestoreNative: %v", err)
	}
	env := string(mustRead(t, filepath.Join(dir, ".env")))
	if strings.Contains(env, "GEMINI_API_KEY") || strings.Contains(env, "GOOGLE_GEMINI_BASE_URL") {
		t.Fatalf("proxy keys survived RestoreNative:\n%s", env)
	}
	if !strings.Contains(env, "KEEP=1") {
		t.Fatalf("unmanaged key was dropped:\n%s", env)
	}
	settings := string(mustRead(t, filepath.Join(dir, "settings.json")))
	if strings.Contains(settings, "_anypickManaged") {
		t.Fatalf("managed marker survived:\n%s", settings)
	}
}

// TestGeminiRestoreNativeKeepsUnlistedKeys: without a marker there is nothing
// that proves a key came from AnyPick, so a user's GEMINI_API_KEY must survive.
func TestGeminiRestoreNativeKeepsUnlistedKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("ANYPICK_HOME", t.TempDir())
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=user-own-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := newGemini(home)
	if err := a.RestoreNative(); err != nil {
		t.Fatalf("RestoreNative: %v", err)
	}
	env := string(mustRead(t, filepath.Join(dir, ".env")))
	if !strings.Contains(env, "GEMINI_API_KEY=user-own-key") {
		t.Fatalf("user key was destroyed:\n%s", env)
	}
}
