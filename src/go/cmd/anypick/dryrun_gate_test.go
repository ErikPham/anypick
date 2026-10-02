package main

// CLI parity part 2, slice A: --dry-run breadth on mutating commands and the
// --yes confirmation gate. Regressions pin three properties:
//
//  1. Every new dry-run prints the TS action string/extra fields (human
//     "[dry-run]" line on stdout; --json exactly one {"dryRun":true,...} value).
//  2. Non-proliferation: DB bytes + ANYPICK_HOME tree + journal rows are
//     byte-equal across a dry-run, and dry-run proceeds while the relevant
//     provider mutation lock is held elsewhere (lock purity, invariant #3).
//  3. confirmDelete gates: non-TTY without --yes exits 2 (CONFIRMATION_REQUIRED),
//     consumes no stdin, mutates nothing; --yes proceeds.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// treeDigest hashes every regular file under root (path + bytes), so any
// write — DB row, runtime marker, journal entry — changes the digest.
func treeDigest(t *testing.T, roots ...string) string {
	t.Helper()
	h := sha256.New()
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			if info.Mode()&os.ModeCharDevice != 0 || info.Size() > 32<<20 {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			fmt.Fprintf(h, "%s\x00%d\x00", path, len(raw))
			h.Write(raw)
			return nil
		})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// journalRows counts operation journal entries straight from SQLite.
func journalRows(t *testing.T) int64 {
	t.Helper()
	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	var n int64
	if err := a.Store.DB.QueryRow(`SELECT COUNT(*) FROM operation_journal`).Scan(&n); err != nil {
		t.Fatalf("journal count: %v", err)
	}
	return n
}

// seedSliceARoot gives every probe a live gateway, account, project binding,
// and global binding.
func seedSliceARoot(t *testing.T) {
	t.Helper()
	seedRootForParity(t)
	for _, args := range [][]string{
		{"use", "claude", "--with", "or"},          // global binding for reset
		{"link", "claude", "--with", "gateway/or"}, // project binding for unlink
	} {
		code := -1
		captureBoth(t, func() { code = run(args) })
		if code != 0 {
			t.Fatalf("seed %v exited %d", args, code)
		}
	}
}

func TestDryRunBreadthLeavesNothingBehind(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedSliceARoot(t)

	home := os.Getenv("HOME")
	data := os.Getenv("ANYPICK_HOME")
	cases := []struct {
		name string
		args []string
		want string // expected stdout substring (human mode)
	}{
		{"account remove", []string{"account", "remove", "deepseek", "ds"}, "[dry-run] Would remove deepseek/ds"},
		{"account export", []string{"account", "export", "deepseek", "ds", "--out", filepath.Join(t.TempDir(), "x.json")}, "[dry-run] Would export deepseek/ds"},
		{"account import", []string{"account", "import", "deepseek", "ds", "envelope.json"}, "[dry-run] Would import deepseek/ds from envelope.json"},
		{"gateway edit", []string{"gateway", "edit", "or", "--endpoint", "https://x.example"}, "[dry-run] Would update gateway or"},
		{"gateway remove", []string{"gateway", "remove", "or"}, "[dry-run] Would remove gateway or"},
		{"gateway reset", []string{"gateway", "reset", "or", "--client", "claude"}, "[dry-run] reset claude"},
		{"preset remove", []string{"preset", "remove", "nope"}, "[dry-run] Would remove preset @nope"},
		{"unlink client", []string{"unlink", "claude"}, "[dry-run] Would remove 1 project binding(s)"},
		{"reset", []string{"reset", "claude"}, "Would remove global binding for claude"},
		{"proxy enable", []string{"proxy", "enable", "deepseek", "ds"}, "[dry-run] Would enable deepseek/ds"},
		{"proxy disable", []string{"proxy", "disable", "deepseek", "ds"}, "[dry-run] Would disable deepseek/ds"},
		{"proxy config", []string{"proxy", "config", "deepseek", "ds"}, "[dry-run] Would configure deepseek/ds"},
		{"proxy start", []string{"proxy", "start", "deepseek", "ds"}, "[dry-run] Would start deepseek/ds"},
		{"proxy stop", []string{"proxy", "stop", "deepseek", "ds"}, "[dry-run] Would stop deepseek/ds"},
		{"pool enable", []string{"proxy", "pool", "enable", "deepseek"}, "[dry-run] Would enable the deepseek multi-account pool"},
		{"pool disable", []string{"proxy", "pool", "disable", "deepseek"}, "[dry-run] Would disable the deepseek pool"},
		{"pool member", []string{"proxy", "pool", "member", "deepseek", "ds", "on"}, "[dry-run] Would enable deepseek/ds"},
		{"hub enable", []string{"proxy", "hub", "enable", "h1"}, "[dry-run] Would enable the unified Proxy Hub"},
		{"hub source", []string{"proxy", "hub", "source", "h1", "deepseek", "ds"}, "[dry-run] Would enable deepseek/ds behind Proxy Hub"},
		{"hub owner", []string{"proxy", "hub", "owner", "h1", "some-model", "deepseek/ds"}, "[dry-run] Would route some-model through deepseek/ds"},
		{"hub stop", []string{"proxy", "hub", "stop", "h1"}, "[dry-run] Would stop the unified Proxy Hub"},
		{"link plan", []string{"link", "claude", "--with", "or"}, "Dry run — project link plan:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer resetGlobals()()
			rows := journalRows(t)
			digest := treeDigest(t, home, data)
			code := -1
			captureBoth(t, func() { code = run(append([]string{"--dry-run"}, tc.args...)) })
			out := lastStdout
			if code != 0 {
				t.Fatalf("--dry-run %v exited %d (stdout=%q)", tc.args, code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("--dry-run %v stdout = %q, want substring %q", tc.args, out, tc.want)
			}
			if after := treeDigest(t, home, data); after != digest {
				t.Fatalf("--dry-run %v mutated ~/.anypick or $HOME state", tc.args)
			}
			if after := journalRows(t); after != rows {
				t.Fatalf("--dry-run %v grew the operation journal: %d → %d rows", tc.args, rows, after)
			}
		})
	}
}

// TestDryRunJSONShapes pins the TS printDryRunCommand contract per action.
func TestDryRunJSONShapes(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedSliceARoot(t)

	cases := []struct {
		name        string
		args        []string
		action      string
		wantMembers map[string]any
	}{
		{"account remove", []string{"account", "remove", "deepseek", "ds"}, "account.remove",
			map[string]any{"provider": "deepseek", "name": "ds"}},
		{"account import", []string{"account", "import", "deepseek", "ds", "env.json", "--force"}, "account.import",
			map[string]any{"provider": "deepseek", "name": "ds", "input": "env.json", "force": true}},
		{"gateway edit", []string{"gateway", "edit", "or", "--model", "m1"}, "gateway.edit",
			map[string]any{"name": "or", "hasApiKey": false}},
		{"gateway remove", []string{"gateway", "remove", "or"}, "gateway.remove",
			map[string]any{"name": "or"}},
		{"preset remove", []string{"preset", "remove", "p1"}, "preset.remove",
			map[string]any{"name": "p1"}},
		{"unlink", []string{"unlink", "claude"}, "binding.unlink",
			map[string]any{"client": "claude", "removed": float64(1)}},
		{"proxy disable", []string{"proxy", "disable", "deepseek", "ds"}, "proxy.disable",
			map[string]any{"provider": "deepseek", "name": "ds"}},
		{"pool disable", []string{"proxy", "pool", "disable", "deepseek"}, "proxy.pool.disable",
			map[string]any{"provider": "deepseek"}},
		{"hub stop", []string{"proxy", "hub", "stop", "h1"}, "proxy.hub.stop", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer resetGlobals()()
			code := -1
			captureBoth(t, func() { code = run(append([]string{"--json", "--dry-run"}, tc.args...)) })
			if code != 0 {
				t.Fatalf("--json --dry-run %v exited %d", tc.args, code)
			}
			dec := json.NewDecoder(strings.NewReader(lastStdout))
			var v map[string]any
			if err := dec.Decode(&v); err != nil {
				t.Fatalf("stdout is not JSON (%v): %.300q", err, lastStdout)
			}
			if _, terr := dec.Token(); terr != io.EOF {
				t.Fatalf("trailing bytes after JSON value: %.200q", lastStdout)
			}
			if v["dryRun"] != true {
				t.Fatalf("dryRun flag missing: %v", v)
			}
			if v["action"] != tc.action {
				t.Fatalf("action = %v, want %q", v["action"], tc.action)
			}
			for k, want := range tc.wantMembers {
				got, ok := v[k]
				if !ok {
					t.Fatalf("member %q missing from %v", k, v)
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("member %q = %v, want %v", k, got, want)
				}
			}
		})
	}

	// Reset-shaped results carry client/removedGlobal/dryRun instead of action.
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"reset", []string{"reset", "claude"}},
		{"gateway reset", []string{"gateway", "reset", "or", "--client", "claude"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer resetGlobals()()
			code := -1
			captureBoth(t, func() { code = run(append([]string{"--json", "--dry-run"}, tc.args...)) })
			if code != 0 {
				t.Fatalf("%v exited %d", tc.args, code)
			}
			var v struct {
				Client        string `json:"client"`
				RemovedGlobal bool   `json:"removedGlobal"`
				DryRun        bool   `json:"dryRun"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(lastStdout)), &v); err != nil {
				t.Fatalf("decode %q: %v", lastStdout, err)
			}
			if !v.DryRun || v.Client != "claude" || !v.RemovedGlobal {
				t.Fatalf("reset result = %+v (stderr %.100q)", v, lastStderr)
			}
		})
	}

	// link keeps its own plan shape: {client, source, dryRun, scope}.
	t.Run("link", func(t *testing.T) {
		defer resetGlobals()()
		code := -1
		captureBoth(t, func() { code = run([]string{"--json", "--dry-run", "link", "claude", "--with", "or"}) })
		if code != 0 {
			t.Fatalf("link dry-run exited %d", code)
		}
		var v struct {
			Client string `json:"client"`
			Source string `json:"source"`
			DryRun bool   `json:"dryRun"`
			Scope  string `json:"scope"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(lastStdout)), &v); err != nil {
			t.Fatalf("decode %q: %v", lastStdout, err)
		}
		if !v.DryRun || v.Client != "claude" || v.Scope != "project" || v.Source == "" {
			t.Fatalf("link result = %+v", v)
		}
	})
}

// TestDryRunProceedsWithMutationLockHeld proves lock purity: a dry-run must
// not acquire the provider mutation scope, so it completes while another
// holder owns it (a lock-taking implementation deadlocks and hits the timeout).
func TestDryRunProceedsWithMutationLockHeld(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	root := a.Root
	closeFn()

	holderReady := make(chan struct{})
	release := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		holderErr <- lock.WithMutationLock(context.Background(), root, refs.ProviderScope("deepseek"),
			func(c context.Context) error {
				close(holderReady)
				select {
				case <-c.Done():
				case <-release:
				}
				return nil
			})
	}()
	<-holderReady
	defer func() {
		close(release)
		select {
		case <-holderErr:
		case <-time.After(waitBudget):
			t.Error("lock holder never released")
		}
	}()

	done := make(chan int, 1)
	go func() {
		restore := resetGlobals()
		code := -1
		captureBoth(t, func() { code = run([]string{"--dry-run", "account", "remove", "deepseek", "ds"}) })
		restore()
		done <- code
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("dry-run under held provider lock exited %d", code)
		}
	case <-time.After(waitBudget):
		t.Fatal("dry-run blocked on the held provider mutation lock — planner purity violated")
	}

	// Same guarantee for a proxy mutation path.
	done2 := make(chan int, 1)
	go func() {
		restore := resetGlobals()
		code := -1
		captureBoth(t, func() { code = run([]string{"--dry-run", "proxy", "disable", "deepseek", "ds"}) })
		restore()
		done2 <- code
	}()
	select {
	case code := <-done2:
		if code != 0 {
			t.Fatalf("proxy disable dry-run under held lock exited %d", code)
		}
	case <-time.After(waitBudget):
		t.Fatal("proxy dry-run blocked on the held provider mutation lock")
	}
}

// TestNonTTYConfirmationGate ports commands.ts confirmDelete semantics:
// non-TTY refuses with exit 2 before consuming stdin or mutating anything;
// --yes proceeds. TS gates ONLY account.remove and gateway.remove this way —
// preset remove / unlink / reset have no confirmation upstream, so they are
// deliberately not gated here (divergence from the slice brief, matching the
// TypeScript authority).
func TestNoInputConfirmationGate(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	seedRootForParity(t)
	defer resetGlobals()()
	code := -1
	captureBoth(t, func() { code = run([]string{"--no-input", "account", "remove", "deepseek", "ds"}) })
	if code != int(anyerr.InvalidUsage) {
		t.Fatalf("--no-input account remove exited %d, want %d", code, int(anyerr.InvalidUsage))
	}
	if !strings.Contains(lastStderr, "requires --yes") {
		t.Fatalf("stderr missing CONFIRMATION_REQUIRED: %.200q", lastStderr)
	}
}

func TestJSONUnknownCommandIsOneValue(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	code := -1
	captureBoth(t, func() { code = run([]string{"--json", "definitely-not-a-command"}) })
	if code != int(anyerr.InvalidUsage) {
		t.Fatalf("exited %d, want InvalidUsage", code)
	}
	dec := json.NewDecoder(strings.NewReader(lastStdout))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, lastStdout)
	}
	if v["ok"] != false {
		t.Fatalf("ok = %v", v["ok"])
	}
}

func TestNonTTYConfirmationGate(t *testing.T) {
	seedThenRemove := func(t *testing.T, args []string) int {
		t.Helper()
		isolateEnv(t)
		clearGlobalsForParity()
		seedRootForParity(t)
		code := -1
		captureBoth(t, func() { code = run(args) })
		return code
	}

	t.Run("account remove requires yes", func(t *testing.T) {
		defer resetGlobals()()
		code := seedThenRemove(t, []string{"account", "remove", "deepseek", "ds"})
		if code != int(anyerr.InvalidUsage) {
			t.Fatalf("non-TTY account remove exited %d, want %d", code, int(anyerr.InvalidUsage))
		}
		if !strings.Contains(lastStderr, "requires --yes") {
			t.Fatalf("stderr missing CONFIRMATION_REQUIRED message: %.200q", lastStderr)
		}
		if strings.Contains(lastStdout, "removed") {
			t.Fatalf("nothing may be removed: stdout %.200q", lastStdout)
		}
		// The account must still exist.
		a, closeFn, err := appReadyForTest()
		if err != nil {
			t.Fatal(err)
		}
		defer closeFn()
		acct, gerr := a.Store.Get(context.Background(), "deepseek", "ds")
		if gerr != nil || acct == nil {
			t.Fatalf("account was removed without --yes: %+v err=%v", acct, gerr)
		}
	})

	t.Run("yes proceeds", func(t *testing.T) {
		defer resetGlobals()()
		code := seedThenRemove(t, []string{"--yes", "account", "remove", "deepseek", "ds"})
		if code != 0 {
			t.Fatalf("--yes account remove exited %d (stderr %.200q)", code, lastStderr)
		}
	})

	t.Run("gateway remove requires yes", func(t *testing.T) {
		defer resetGlobals()()
		code := seedThenRemove(t, []string{"gateway", "remove", "or"})
		if code != int(anyerr.InvalidUsage) {
			t.Fatalf("non-TTY gateway remove exited %d, want %d", code, int(anyerr.InvalidUsage))
		}
		if !strings.Contains(lastStderr, "requires --yes") {
			t.Fatalf("stderr missing gate message: %.200q", lastStderr)
		}
	})

	t.Run("json stays pure when gated", func(t *testing.T) {
		defer resetGlobals()()
		code := seedThenRemove(t, []string{"--json", "account", "remove", "deepseek", "ds"})
		if code != int(anyerr.InvalidUsage) {
			t.Fatalf("gated --json remove exited %d", code)
		}
		if strings.TrimSpace(lastStdout) != "" {
			t.Fatalf("error path must keep stdout machine-pure, got %.200q", lastStdout)
		}
	})

	t.Run("CI env does not bypass the gate", func(t *testing.T) {
		defer resetGlobals()()
		t.Setenv("CI", "1")
		code := seedThenRemove(t, []string{"account", "remove", "deepseek", "ds"})
		if code != int(anyerr.InvalidUsage) {
			t.Fatalf("CI=1 non-TTY remove exited %d", code)
		}
	})
}
