package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every path helper the runtime snapshots must resolve under the given
// home, never at a product-dir override or the live home.
func TestManagedConfigPathsStayUnderHome(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	t.Setenv("WORKBUDDY_CONFIG_DIR", "")
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_CONFIG_DIR", "")

	home := t.TempDir()
	cases := map[string][]string{
		"workbuddy": WorkBuddyModelsPaths(home),
		"gemini":    GeminiConfigPaths(home),
		"opencode":  OpenCodeConfigPaths(home),
		"dsh":       DshConfigPaths(home),
		"zcode":     ZCodeConfigPaths(home),
	}
	for clientID, paths := range cases {
		if len(paths) == 0 {
			t.Fatalf("%s paths empty", clientID)
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, home) {
				t.Fatalf("%s path escapes home: %q", clientID, p)
			}
		}
	}
	// WorkBuddy resolves both product roots when they exist.
	for _, dir := range []string{".workbuddy-ai", ".codebuddy"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := WorkBuddyModelsPaths(home); len(got) != 2 {
		t.Fatalf("workbuddy paths with both roots = %v, want 2", got)
	}
}
