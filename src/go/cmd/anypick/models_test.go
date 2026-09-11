package main

import (
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// TestModelsListsCatalogIDsForAGatewaySource covers the question the
// MODEL_NOT_PINNED warning asks the user to answer by hand.
func TestModelsListsCatalogIDsForAGatewaySource(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "claude", "--with", "gateway/or"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("models exited %d (stdout=%q stderr=%q)", exit, lastStdout, lastStderr)
	}
	ids := nonEmptyLines(lastStdout)
	if len(ids) == 0 {
		t.Fatal("no model ids listed for a seeded gateway")
	}
	found := false
	for _, id := range ids {
		if id == "openai/gpt-5.6-sol" {
			found = true
		}
		if strings.ContainsAny(id, " \t") {
			t.Fatalf("stdout must be one bare id per line, got %q", id)
		}
	}
	if !found {
		t.Fatalf("openrouter catalog id missing from %v", ids)
	}
	// Context is chatter: stderr by default, so stdout stays pipeable.
	// DisplayRef names the gateway itself ("or"), not the "gateway/or" ref.
	for _, want := range []string{"claude", "or", "openrouter catalog"} {
		if !strings.Contains(lastStderr, want) {
			t.Fatalf("context line %q lacks %q", lastStderr, want)
		}
	}
}

// TestModelsDefaultsToTheCurrentBinding: without --with the client's persisted
// binding is the source, which is how the warning's user would call it.
func TestModelsDefaultsToTheCurrentBinding(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() { exit = run([]string{"use", "claude", "--with", "gateway/or"}) })
	if exit != 0 {
		t.Fatalf("use exited %d (%q)", exit, lastStderr)
	}
	captureBoth(t, func() { exit = run([]string{"models", "claude"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("models exited %d (%q)", exit, lastStderr)
	}
	fromBinding := nonEmptyLines(lastStdout)

	captureBoth(t, func() { exit = run([]string{"models", "claude", "--with", "gateway/or"}) })
	fromFlag := nonEmptyLines(lastStdout)

	if strings.Join(fromBinding, ",") != strings.Join(fromFlag, ",") {
		t.Fatalf("binding source and --with disagree:\n%v\n%v", fromBinding, fromFlag)
	}
}

// TestModelsQuietKeepsOnlyIDs: --quiet silences chatter, never data.
func TestModelsQuietKeepsOnlyIDs(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	globals.Quiet = true
	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "claude", "--with", "gateway/or"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("quiet models exited %d", exit)
	}
	if strings.TrimSpace(lastStderr) != "" {
		t.Fatalf("--quiet must silence the context line, got %q", lastStderr)
	}
	if len(nonEmptyLines(lastStdout)) == 0 {
		t.Fatal("--quiet must still print the ids")
	}
}

// A missing route is an error, not an empty list: "nothing to pin" and "no
// connection" are different answers.
func TestModelsWithoutRouteFails(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "claude"}) })
	if exit == int(anyerr.Success) {
		t.Fatalf("models without a binding must fail, stdout=%q", lastStdout)
	}
	if !strings.Contains(lastStderr, "--with") {
		t.Fatalf("failure must say how to supply a source, got %q", lastStderr)
	}
}

func TestModelsUnknownClientFails(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	var exit int
	captureBoth(t, func() { exit = run([]string{"models", "nope-client", "--with", "gateway/or"}) })
	if exit == int(anyerr.Success) {
		t.Fatal("an unknown client must fail")
	}
}

// The pinned model stays listed even when the catalog does not know it: the
// user must be able to re-select what the connection is already running.
//
// The id is deliberately absent from every catalog — a pinnable-but-unknown id
// is the only input that can tell "merged the binding" apart from "printed the
// catalog".
func TestModelsKeepsAPinnedModel(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()
	seedRootForParity(t)

	const pinned = "zz-pinned-not-in-any-catalog"
	var exit int
	captureBoth(t, func() {
		exit = run([]string{"use", "claude", "--with", "gateway/or", "--model", pinned})
	})
	if exit != 0 {
		t.Fatalf("use exited %d (%q)", exit, lastStderr)
	}
	captureBoth(t, func() { exit = run([]string{"models", "claude"}) })
	if exit != int(anyerr.Success) {
		t.Fatalf("models exited %d", exit)
	}
	found := false
	for _, id := range nonEmptyLines(lastStdout) {
		if id == pinned {
			found = true
		}
	}
	if !found {
		t.Fatalf("pinned model %q missing from %q", pinned, lastStdout)
	}
}
