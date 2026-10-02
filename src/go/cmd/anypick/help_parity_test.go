package main

// NATIVE-M1 CLI parity part 1: structural command-tree golden gate against
// go/testdata/parity/cli-tree.json, stdout purity under --json,
// verbosity-flag consumers, and dynamic positional completion. The fixture
// originated in the TypeScript commander program; it is maintained by hand
// now, and the enforced part is the path set (descriptions and flags are
// documentation kept in step with the Go dispatch).

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

type cliTreeFixture struct {
	Commands []struct {
		Path        string   `json:"path"`
		Description string   `json:"description"`
		Flags       []string `json:"flags"`
		Subcommands []string `json:"subcommands"`
		Hidden      bool     `json:"hidden"`
	} `json:"commands"`
	Omitted []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"omitted"`
}

func loadCliTree(t *testing.T) cliTreeFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "parity", "cli-tree.json"))
	if err != nil {
		t.Fatalf("read the CLI tree fixture: %v", err)
	}
	var f cliTreeFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f cliTreeFixture) omittedSet() map[string]bool {
	out := map[string]bool{}
	for _, o := range f.Omitted {
		out[o.Path] = true
	}
	return out
}

// dispatchedTopLevel extracts the string case literals of the switch inside
// run() in main.go straight from the AST, so the test compares the fixture
// against the real dispatch tree instead of a drift-prone copy.
func dispatchedTopLevel(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			return true
		}
		ast.Inspect(fn, func(m ast.Node) bool {
			sw, ok := m.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok || len(cc.List) != 1 {
					continue
				}
				lit, ok := cc.List[0].(*ast.BasicLit)
				if ok && lit.Kind == token.STRING {
					cases[strings.Trim(lit.Value, "`\"")] = true
				}
			}
			return true
		})
		return false
	})
	return cases
}

// clearGlobalsForParity zeroes recorded global flags — resetGlobals() only
// snapshots for a deferred restore, it never clears leaked state.
func clearGlobalsForParity() {
	globals.JSON, globals.DryRun, globals.NoInput, globals.Yes = false, false, false, false
	globals.Quiet, globals.Verbose, globals.Trace, globals.Reveal, globals.Tray = false, false, false, false, false
	globalJSON = false
}

// globalsClean asserts no global flag survived a probe — the "tray start"
// --dry-run probe used to leak DryRun=true into every later probe.
func globalsClean() bool {
	return !globals.JSON && !globals.DryRun && !globals.NoInput && !globals.Yes &&
		!globals.Quiet && !globals.Verbose && !globals.Trace && !globals.Reveal &&
		!globals.Tray && !globalJSON
}

// captureBoth redirects stdout and stderr for the duration of fn, returning
// (stdout, stderr) and mirroring them into lastStdout/lastStderr.
var lastStdout, lastStderr string

func captureBoth(t *testing.T, fn func()) (string, string) {
	t.Helper()
	soutR, soutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	serrR, serrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = soutW, serrW
	fn()
	os.Stdout, os.Stderr = savedOut, savedErr
	_ = soutW.Close()
	_ = serrW.Close()
	outData, _ := io.ReadAll(soutR)
	errData, _ := io.ReadAll(serrR)
	lastStdout, lastStderr = string(outData), string(errData)
	return lastStdout, lastStderr
}

var unknownSentinels = []string{
	"unknown command",
	"unknown gateway subcommand",
	"unknown account subcommand",
	"unknown preset subcommand",
	"unknown plugin subcommand",
	"unknown codex subcommand",
	"unknown codex profile subcommand",
}

func looksUnknown(out, errOut string) bool {
	joined := out + errOut
	for _, s := range unknownSentinels {
		if strings.Contains(joined, s) {
			return true
		}
	}
	return false
}

// probeExtra holds per-path benign arguments that keep probing side-effect
// free (the handlers validate arity before acting; these two would not).
var probeExtra = map[string][]string{
	"tray start": {"--dry-run"}, // plan only: never spawns the supervisor
}

func TestCliTreePathsResolveInGoDispatch(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	fixture := loadCliTree(t)
	omitted := fixture.omittedSet()
	dispatch := dispatchedTopLevel(t)

	// Sentinel: structural drift in main.go's switch must fail closed, so the
	// extraction is required to still see these key single-literal cases.
	for _, key := range []string{"use", "__complete", "tray"} {
		if !dispatch[key] {
			t.Errorf("dispatchedTopLevel lost case %q — AST extraction drifted", key)
		}
	}

	for _, cmd := range fixture.Commands {
		path := cmd.Path
		if path == "" || omitted[path] || omitted[strings.SplitN(path, " ", 2)[0]] {
			continue
		}
		if path == "update" {
			// cmdUpdate hits the GitHub releases API before any flag gate;
			// probe it structurally instead of over the network.
			if !dispatch["update"] {
				t.Errorf("fixture command %q missing from Go dispatch tree", path)
			}
			continue
		}
		args := strings.Split(path, " ")
		args = append(args, probeExtra[path]...)
		restore := resetGlobals()
		out, errOut := captureBoth(t, func() { run(args) })
		restore()
		if !globalsClean() {
			t.Errorf("probe %q leaked global-flag state into subsequent probes: %+v", path, globals)
		}
		if looksUnknown(out, errOut) {
			t.Errorf("fixture command %q does not resolve in the Go dispatch tree (stdout=%q stderr=%q)",
				path, out, errOut)
		}
	}
	resetGlobals()
}

func TestGoDispatchTreeMatchesTypeScriptTopLevel(t *testing.T) {
	fixture := loadCliTree(t)
	omitted := fixture.omittedSet()
	tsTop := map[string]bool{}
	for _, cmd := range fixture.Commands {
		if cmd.Path == "" {
			continue
		}
		tsTop[strings.SplitN(cmd.Path, " ", 2)[0]] = true
	}
	for c := range dispatchedTopLevel(t) {
		if strings.HasPrefix(c, "-") || c == "help" || c == "--tui" || c == "tui" {
			continue // flags and the documented TUI alias
		}
		if tsTop[c] || omitted[c] {
			continue
		}
		t.Errorf("Go dispatches top-level command %q but TypeScript neither knows it nor lists it as omitted", c)
	}
}

func TestUnknownCommandPrintsUsageAndInvalidUsage(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	exit := -1
	out, _ := captureBoth(t, func() { exit = run([]string{"definitely-not-a-command"}) })
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("unknown command must print usage on stdout, got %.200q", out)
	}
	if exit != int(anyerr.InvalidUsage) {
		t.Fatalf("unknown command exited %d, want InvalidUsage (%d)", exit, int(anyerr.InvalidUsage))
	}
}

// seedRootForParity gives probes a live gateway and account.
func seedRootForParity(t *testing.T) {
	t.Helper()
	t.Setenv("ANYPICK_SECRET", "sk-parity-seed-12345678")
	seed := func(args []string) {
		out, errOut := "", ""
		code := -1
		func() {
			out, errOut = captureBoth(t, func() { code = run(args) })
		}()
		if code != 0 {
			t.Fatalf("seed %v exited %d (stdout=%q stderr=%q)", args, code, out, errOut)
		}
	}
	seed([]string{"gateway", "add", "or", "--provider", "openrouter"})
	seed([]string{"add", "deepseek", "--name", "ds", "--kind", "api-key"})
}

func TestDynamicCompletionKinds(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	wantProviders := append([]string(nil), a.Providers.IDs()...)
	wantClients := append([]string(nil), a.Clients.IDs()...)
	closeFn()

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"providers", []string{"__complete", "providers"}, wantProviders},
		{"clients", []string{"__complete", "clients"}, wantClients},
		{"gateways", []string{"__complete", "gateways"}, []string{"or"}},
		{"accounts for provider", []string{"__complete", "accounts", "deepseek"}, []string{"ds"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer resetGlobals()()
			var out string
			captureBoth(t, func() { run(tc.args) })
			out = lastStdout
			got := nonEmptyLines(out)
			if len(got) != len(tc.want) {
				t.Fatalf("__complete %s = %v, want %v", strings.Join(tc.args[1:], " "), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("__complete %s = %v, want %v", strings.Join(tc.args[1:], " "), got, tc.want)
				}
			}
		})
	}

	// Unknown kind fails without printing ids.
	var out string
	captureBoth(t, func() { run([]string{"__complete", "bogus"}) })
	out = lastStdout
	if len(nonEmptyLines(out)) != 0 {
		t.Fatalf("unknown kind printed %q", out)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestCompletionScriptsShellOutToCompleteHelper(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var out string
		captureBoth(t, func() { run([]string{"completion", shell}) })
		out = lastStdout
		for _, want := range []string{"__complete clients", "__complete providers", "__complete accounts"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s completion script lacks positional %q call", shell, want)
			}
		}
	}
}

// TestJSONStdoutPurityMatrix asserts every machine-readable invocation prints
// exactly ONE JSON value on stdout with zero trailing bytes (plan M8:
// "JSON stdout is exactly one parseable result stream").
func TestJSONStdoutPurityMatrix(t *testing.T) {
	clearGlobalsForParity()
	exportFile := filepath.Join(t.TempDir(), "export.json")
	cases := []struct {
		name    string
		args    []string
		wantKey string
	}{
		{"providers", []string{"providers"}, ""},
		{"clients", []string{"clients"}, ""},
		{"doctor", []string{"doctor"}, ""},
		{"current", []string{"current", "deepseek"}, ""},
		{"proxy status", []string{"proxy", "status", "deepseek", "ds"}, ""},
		{"use dry-run", []string{"use", "claude", "--with", "or", "--dry-run"}, ""},
		{"models", []string{"models", "claude", "--with", "gateway/or"}, ""},
		{"supervisor status", []string{"status"}, ""},
		{"supervisor stop", []string{"stop"}, ""},
		{"preset list", []string{"preset", "list"}, ""},
		{"account export", []string{"account", "export", "deepseek", "ds", "--out", exportFile}, ""},
		{"gateway list", []string{"gateway", "list"}, "gateways"},
		{"gateway edit", []string{"gateway", "edit", "or", "--model", "m-json"}, "name"},
		{"proxy enable", []string{"proxy", "enable", "deepseek", "ds"}, "port"},
		{"proxy disable", []string{"proxy", "disable", "deepseek", "ds"}, "action"},
		{"proxy logs", []string{"proxy", "logs", "deepseek", "ds"}, "lines"},
		{"hub enable", []string{"proxy", "hub", "enable", "parity"}, "hub"},
		{"pool enable", []string{"proxy", "pool", "enable", "deepseek"}, "provider"},
		{"serve stop", []string{"serve", "--stop"}, "state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer resetGlobals()()
			isolateEnv(t) // fresh root per case
			seedRootForParity(t)
			full := append([]string{"--json"}, tc.args...)
			var out string
			exit := -1
			captureBoth(t, func() { exit = run(full) })
			out = lastStdout
			if exit != int(anyerr.Success) {
				t.Fatalf("--json %s exited %d\nstdout=%s\nstderr=%s", tc.name, exit, out, lastStderr)
			}
			dec := json.NewDecoder(strings.NewReader(out))
			var v interface{}
			if err := dec.Decode(&v); err != nil {
				t.Fatalf("--json %s stdout is not JSON (%v): %.300q", tc.name, err, out)
			}
			if _, terr := dec.Token(); terr != io.EOF { // a second token means trailing bytes
				t.Fatalf("--json %s stdout carries trailing non-JSON bytes", tc.name)
			}
			if strings.Contains(out, "sk-parity-seed") {
				t.Fatalf("--json %s printed the seeded secret: %.300q", tc.name, out)
			}
			if tc.wantKey != "" {
				obj, _ := v.(map[string]interface{})
				if obj == nil || obj[tc.wantKey] == nil {
					t.Fatalf("--json %s missing %s: %.300q", tc.name, tc.wantKey, out)
				}
			}
		})
	}
}

func TestQuietSuppressesChatterButNotMutation(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	globals.Quiet = true
	var out string
	exit := -1
	captureBoth(t, func() { exit = run([]string{"use", "claude", "--with", "or"}) })
	out = lastStdout
	if exit != 0 {
		t.Fatalf("quiet use exited %d", exit)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--quiet must suppress chatter lines, got %q", out)
	}
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	binding, gerr := a.BindingSvc.GetGlobalContext(context.Background(), "claude")
	if gerr != nil || binding == nil {
		t.Fatalf("--quiet must still persist the binding: %+v err=%v", binding, gerr)
	}
}

func TestTraceStepsGoToStderrOnly(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	globals.Trace = true
	exit := -1
	stdout, stderr := captureBoth(t, func() { exit = run([]string{"use", "claude", "--with", "gateway/or"}) })
	if exit != 0 {
		t.Fatalf("trace use exited %d", exit)
	}
	if !strings.Contains(stderr, "[trace]") {
		t.Fatalf("--trace must print step lines to stderr, got %.200q", stderr)
	}
	if strings.Contains(stdout, "[trace]") {
		t.Fatalf("--trace leaked into stdout: %q", stdout)
	}
	if !strings.Contains(stdout, "activated") {
		t.Fatalf("human result line missing under --trace: %q", stdout)
	}
}

func TestDoctorDetailRequiresVerbose(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()

	// Plant a dangling active pointer so a finding carries non-empty detail.
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	if serr := a.Store.SetActiveContext(context.Background(), "grok", "ghost"); serr != nil {
		t.Fatal(serr)
	}
	closeFn()
	defer resetGlobals()()

	var out string
	captureBoth(t, func() { run([]string{"doctor"}) })
	out = lastStdout
	if strings.Contains(out, "points at missing account") {
		t.Fatalf("detail must stay hidden without --verbose: %q", out)
	}
	globals.Verbose = true
	captureBoth(t, func() { run([]string{"doctor"}) })
	out = lastStdout
	if !strings.Contains(out, "points at missing account") {
		t.Fatalf("--verbose should reveal check detail: %q", out)
	}
}

// TestCLIReferenceCommandsExist ties docs/src/content/docs/reference/cli.mdx
// to the command tree: every `anypick …` invocation in a bash code block must
// resolve to a known command path. The page had drifted into documenting
// commands and flags the binary never had, and nothing caught it because the
// structural golden only probes the fixture, not the prose.
func TestCLIReferenceCommandsExist(t *testing.T) {
	fixture := loadCliTree(t)
	known := map[string]bool{}
	for _, c := range fixture.Commands {
		known[c.Path] = true
	}
	// Paths deliberately omitted from the fixture (hidden helpers, Go-only
	// commands) are still commands the reference may document.
	for _, o := range fixture.Omitted {
		known[o.Path] = true
	}
	docPath := filepath.Join("..", "..", "..", "..", "docs", "src", "content", "docs", "reference", "cli.mdx")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	var unknown []string
	for _, block := range cliDocCommandLines(string(doc)) {
		path := ""
		for _, token := range strings.Fields(block)[1:] {
			if strings.HasPrefix(token, "-") {
				break
			}
			candidate := token
			if path != "" {
				candidate = path + " " + token
			}
			if !known[candidate] {
				break
			}
			path = candidate
		}
		if path == "" {
			unknown = append(unknown, block)
		}
	}
	if len(unknown) > 0 {
		t.Fatalf("CLI reference documents commands the tree does not have:\n  %s",
			strings.Join(unknown, "\n  "))
	}
}

// cliDocCommandLines returns every `anypick …` line inside a ```bash fence.
func cliDocCommandLines(doc string) []string {
	var out []string
	blocks := strings.Split(doc, "```")
	for i := 1; i < len(blocks); i += 2 {
		block := blocks[i]
		if !strings.HasPrefix(block, "bash\n") {
			continue
		}
		for _, line := range strings.Split(strings.TrimPrefix(block, "bash\n"), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "anypick ") {
				out = append(out, line)
			}
		}
	}
	return out
}
