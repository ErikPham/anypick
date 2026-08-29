package main

// Global-flag matrix regressions (M8): flags are recorded wherever they
// appear before the command, --dry-run plans without persisting, and secret
// redaction follows TS maskSecret unless --reveal.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
)

func resetGlobals() func() {
	old := globals
	return func() { globals = old; globalJSON = old.JSON }
}

// openAppBindingForTest reads the global binding for a client from the
// isolated ANYPICK_HOME.
func openAppBindingForTest(t *testing.T, clientID string) (*bindings.Global, error) {
	a, closeFn, err := appReadyForTest()
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return a.Bindings.GetGlobal(clientID)
}

// captureStdout redirects os.Stdout for the duration of fn.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = saved
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestGlobalFlagMatrixRecorded(t *testing.T) {
	isolateEnv(t)
	defer resetGlobals()()
	args := extractGlobals([]string{
		"--json", "--dry-run", "--no-input", "--yes", "--quiet",
		"--verbose", "--trace", "--reveal", "--tray", "providers",
	})
	if len(args) != 1 || args[0] != "providers" {
		t.Fatalf("command args lost: %v", args)
	}
	if !globals.JSON || !globals.DryRun || !globals.NoInput || !globals.Yes ||
		!globals.Quiet || !globals.Verbose || !globals.Trace || !globals.Reveal || !globals.Tray {
		t.Fatalf("flags not all recorded: %+v", globals)
	}
	if !globalJSON {
		t.Fatal("globalJSON alias not set")
	}
}

func TestUseDryRunPlansWithoutPersisting(t *testing.T) {
	isolateEnv(t)
	defer resetGlobals()()
	globals.DryRun = true

	// Seed a gateway so the plan resolves a real source.
	t.Setenv("ANYPICK_SECRET", "sk-dry-run-secret-12345")
	if code := run([]string{"gateway", "add", "drygw", "--provider", "openrouter"}); code != 0 {
		t.Fatalf("gateway seed exited %d", code)
	}
	globals.DryRun = true // add may have consumed nothing; keep explicit

	if code := run([]string{"use", "claude", "--with", "gateway/drygw"}); code != 0 {
		t.Fatalf("use --dry-run exited %d", code)
	}

	// The global binding must NOT exist: dry runs write nothing.
	binding, gerr := openAppBindingForTest(t, "claude")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if binding != nil {
		t.Fatalf("dry run persisted a binding: %+v", binding)
	}
}

func TestGatewayShowRedactsUnlessRevealed(t *testing.T) {
	isolateEnv(t)
	defer resetGlobals()()
	t.Setenv("ANYPICK_SECRET", "sk-live-key-abcdef1234567890")
	if code := run([]string{"gateway", "add", "redactgw", "--provider", "openrouter"}); code != 0 {
		t.Fatalf("gateway seed exited %d", code)
	}

	// Default: masked.
	out := captureStdout(t, func() {
		if code := run([]string{"gateway", "show", "redactgw"}); code != 0 {
			t.Fatalf("show exited %d", code)
		}
	})
	if strings.Contains(out, "sk-live-key") {
		t.Fatalf("secret leaked in default show: %s", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("expected masked key formatting: %s", out)
	}

	// --reveal shows the real value.
	globals.Reveal = true
	out = captureStdout(t, func() {
		if code := run([]string{"--reveal", "gateway", "show", "redactgw"}); code != 0 {
			t.Fatalf("revealed show exited %d", code)
		}
	})
	if !strings.Contains(out, "sk-live-key-abcdef1234567890") {
		t.Fatalf("--reveal did not reveal: %s", out)
	}
	globals.Reveal = false
}

func TestMaskSecretMatchesTS(t *testing.T) {
	if got := maskSecret(""); got != "(empty)" {
		t.Fatalf("empty: %q", got)
	}
	if got := maskSecret("short"); got != "••••••••" {
		t.Fatalf("short: %q", got)
	}
	if got := maskSecret("12345678"); got != "••••••••" {
		t.Fatalf("8 chars: %q", got)
	}
	if got := maskSecret("123456789"); got != "1234…6789" {
		t.Fatalf("long: %q", got)
	}
}

func TestTrayDryRunDoesNotSpawn(t *testing.T) {
	isolateEnv(t)
	defer resetGlobals()()
	globals.DryRun = true
	if code := run([]string{"start"}); code != 0 {
		t.Fatalf("start dry-run exited %d", code)
	}
	dir := supervisorRuntimeDir(os.Getenv("ANYPICK_HOME"))
	if _, err := os.Stat(filepath.Join(dir, "service.pid")); !os.IsNotExist(err) {
		t.Fatalf("dry run spawned a supervisor: %v", err)
	}
	if code := run([]string{"stop"}); code != 0 {
		t.Fatalf("stop dry-run exited %d", code)
	}
}

func TestGlobalFlagsAfterSubcommand(t *testing.T) {
	isolateEnv(t)
	defer resetGlobals()()

	// Seed a gateway, then place the global flag AFTER the command — the
	// documented contract is "before or after the subcommand".
	t.Setenv("ANYPICK_SECRET", "sk-after-sub-12345678")
	if code := run([]string{"gateway", "add", "aftergw", "--provider", "openrouter"}); code != 0 {
		t.Fatalf("gateway seed exited %d", code)
	}
	if code := run([]string{"use", "claude", "--with", "gateway/aftergw", "--dry-run"}); code != 0 {
		t.Fatalf("use … --dry-run exited %d (flag must be recognized after the subcommand)", code)
	}
	if !globals.DryRun {
		t.Fatal("--dry-run not recorded from trailing position")
	}
	binding, gerr := openAppBindingForTest(t, "claude")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if binding != nil {
		t.Fatalf("dry run persisted a binding: %+v", binding)
	}
}

func TestNormalizeGlobalFlagsPreservesPassThrough(t *testing.T) {
	defer resetGlobals()()
	got := normalizeGlobalFlags([]string{
		"run", "--with", "grok/work", "--dry-run", "--", "claude", "--dry-run", "--verbose",
	})
	want := []string{"run", "--with", "grok/work", "--", "claude", "--dry-run", "--verbose"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d: got %q want %q\nall: %v", i, got[i], want[i], got)
		}
	}
	if !globals.DryRun {
		t.Fatalf("flags before -- must be recorded: %+v", globals)
	}
	if globals.Verbose {
		t.Fatal("--verbose after -- must stay pass-through, not be recorded")
	}
}

func TestBareInvocationHonoursConfiguredSurface(t *testing.T) {
	isolateEnv(t)
	defer resetGlobals()()

	// Default (unconfigured): explicit CLI-help policy.
	out := captureStdout(t, func() {
		if code := run(nil); code != 0 {
			t.Fatalf("bare default exited %d", code)
		}
	})
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("default bare invocation should print help, got: %.200q", out)
	}

	// Persisted tui surface: explain where the interactive experience lives
	// and exit successfully — never a partial Go TUI.
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := a.Config.SetLaunchSurface(context.Background(), "tui"); serr != nil {
		t.Fatal(serr)
	}
	closeFn()

	out = captureStdout(t, func() {
		if code := run(nil); code != 0 {
			t.Fatalf("bare tui-surface exited %d", code)
		}
	})
	if !strings.Contains(out, "TypeScript build") {
		t.Fatalf("configured tui surface must explain, got: %.200q", out)
	}

	// Persisted tray surface resolves for dispatch (the app run itself needs
	// a native host; the policy under test is the config read).
	a2, close2, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := a2.Config.SetLaunchSurface(context.Background(), "tray"); serr != nil {
		t.Fatal(serr)
	}
	close2()
	a3, close3, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer close3()
	if got := configuredLaunchSurface(context.Background(), a3); got != "tray" {
		t.Fatalf("configured surface = %q, want tray", got)
	}
}
