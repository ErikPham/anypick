package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// The onboarding docs are the canonical CLI contract (README, getting-started):
//
//	anypick add account codex --current --name personal
//	anypick add account codex --new --name personal
//
// These black-box tests run the documented forms against a sandbox HOME so a
// contract break fails the build instead of the user's first session.
func TestAddAccountCanonicalDocsForms(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	writeCodexLive := func() {
		p := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"OPENAI_API_KEY":"sk-docs-test"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCodexLive()

	runAdd := func(args ...string) int {
		defer resetGlobals()()
		code := -1
		captureBoth(t, func() { code = run(args) })
		return code
	}

	if code := runAdd("add", "account", "codex", "--current", "--name", "personal"); code != int(anyerr.Success) {
		t.Fatalf("add account --current exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	// Documented account is now listed.
	if code := runAdd("list", "accounts", "codex"); code != int(anyerr.Success) {
		t.Fatalf("list accounts exited %d", code)
	}
	if !strings.Contains(lastStdout, "codex/personal") {
		t.Fatalf("list accounts lacks codex/personal (stdout=%q)", lastStdout)
	}
}

// --new must run a real browser sign-in and save the NEW credential, not
// re-snapshot whatever happens to be live. The fake grok launcher writes a
// different identity than anything on disk; the saved account, the live file
// and the active pointer must all carry it afterwards.
func TestAddAccountNewPerformsFreshLogin(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	// Scrub PATH so a host-installed launcher cannot escape the fake.
	t.Setenv("PATH", t.TempDir())

	authPath := filepath.Join(os.Getenv("HOME"), ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", authPath)
	launcher := filepath.Join(t.TempDir(), "fake-grok-login.sh")
	// Absolute tool paths: PATH is scrubbed below so a host-installed
	// launcher cannot escape the fake, which also hides mkdir/dirname.
	body := `/bin/mkdir -p "$(/usr/bin/dirname "` + authPath + `")" && /usr/bin/printf '%s' '{"s1":{"email":"fresh@example.com","access_token":"tok-fresh"}}' > "` + authPath + `"` + "\n"
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_BINARY", launcher)

	runAdd := func(args ...string) int {
		defer resetGlobals()()
		code := -1
		captureBoth(t, func() { code = run(args) })
		return code
	}

	if code := runAdd("add", "account", "grok", "--new", "--name", "work"); code != int(anyerr.Success) {
		t.Fatalf("add account --new exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	if !strings.Contains(lastStdout, "grok/work") {
		t.Fatalf("login did not report the saved account (stdout=%q stderr=%q)", lastStdout, lastStderr)
	}

	a, closeFn, err := appReadyForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	acct, gerr := a.Store.GetContext(context.Background(), "grok", "work")
	if gerr != nil || acct == nil {
		t.Fatalf("saved grok/work missing: %+v %v", acct, gerr)
	}
	if acct.Meta.Identity != "fresh@example.com" {
		t.Fatalf("saved identity = %q, want the fresh login identity", acct.Meta.Identity)
	}
	active, _ := a.Store.GetActiveContext(context.Background(), "grok")
	if active != "work" {
		t.Fatalf("active pointer = %q, want work", active)
	}
	live, rerr := os.ReadFile(authPath)
	if rerr != nil || !strings.Contains(string(live), "fresh@example.com") {
		t.Fatalf("live grok login was not replaced by the fresh one: %s %v", live, rerr)
	}

	// --auth-method selects the browser method; unknown ids fail closed.
	if code := runAdd("add", "account", "grok", "--new", "--name", "w2", "--auth-method", "nope"); code == int(anyerr.Success) {
		t.Fatalf("unknown --auth-method exited 0 (stdout=%q stderr=%q)", lastStdout, lastStderr)
	}
}

func TestAddAccountMutualExclusionAndAliases(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	p := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"OPENAI_API_KEY":"sk-docs-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	runAdd := func(args ...string) int {
		defer resetGlobals()()
		code := -1
		captureBoth(t, func() { code = run(args) })
		return code
	}

	if code := runAdd("add", "account", "codex", "--current", "--new", "--name", "x"); code != int(anyerr.InvalidUsage) {
		t.Fatalf("--current+--new exited %d, want InvalidUsage", code)
	}
	// --new takes no credential flags: it signs in through the browser.
	if code := runAdd("add", "account", "codex", "--new", "--name", "x", "--credential", "api-key"); code != int(anyerr.InvalidUsage) {
		t.Fatalf("--new with credential flags exited %d, want InvalidUsage", code)
	}
	// Deprecated bare form stays a working alias for --current.
	if code := runAdd("add", "codex", "--name", "legacy"); code != int(anyerr.Success) {
		t.Fatalf("deprecated add alias exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	// Docs-era --api-key value form for compatible vendors.
	t.Setenv("ANYPICK_SECRET", "")
	if code := runAdd("add", "account", "deepseek", "--name", "ds", "--api-key", "sk-deepseek-test"); code != int(anyerr.Success) {
		t.Fatalf("add account --api-key exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	// Generic credential form with an endpoint-profile option.
	if code := runAdd("add", "account", "deepseek", "--name", "ds2",
		"--credential", "api-key", "--secret", "sk-x", "--option", "profile=global"); code != int(anyerr.Success) {
		t.Fatalf("add account --credential/--option exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	// Unknown option qualifiers fail closed instead of being dropped.
	if code := runAdd("add", "account", "deepseek", "--name", "ds3",
		"--credential", "api-key", "--secret", "sk-x", "--option", "bogus=1"); code != int(anyerr.InvalidUsage) {
		t.Fatalf("unknown --option exited %d, want InvalidUsage", code)
	}
	// Bare --secret prompts, which refuses without a TTY.
	if !isTTY(os.Stdin) {
		if code := runAdd("add", "account", "deepseek", "--name", "ds4",
			"--credential", "api-key", "--secret"); code != int(anyerr.InvalidUsage) {
			t.Fatalf("bare --secret without TTY exited %d, want InvalidUsage", code)
		}
	}
}
