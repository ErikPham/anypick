package main

import (
	"context"
	"os"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// The reference documents gateway add as:
//
//	anypick add gateway [name] --provider <id> --endpoint <url>
//	    [--api-key <key> | --api-key-env <var>]
//	    [--model <id>] [--models <role=id...>]
func TestGatewayAddKeySourcesAndModels(t *testing.T) {
	isolateEnv(t)
	clearGlobalsForParity()
	defer resetGlobals()()

	runGw := func(args ...string) int {
		defer resetGlobals()()
		code := -1
		captureBoth(t, func() { code = run(args) })
		return code
	}
	getProfile := func(name string) (string, map[string]string) {
		t.Helper()
		a, closeFn, err := appReadyForTest()
		if err != nil {
			t.Fatal(err)
		}
		defer closeFn()
		pr, gerr := a.Runtime.ProfileSvc.GetContext(context.Background(), name)
		if gerr != nil || pr == nil {
			t.Fatalf("gateway %s missing: %v", name, gerr)
		}
		return pr.Meta.DefaultModel, pr.Meta.Models
	}

	t.Setenv("ANYPICK_SECRET", "sk-env-123")
	if code := runGw("add", "gateway", "gw-env", "--provider", "openrouter"); code != int(anyerr.Success) {
		t.Fatalf("env-secret add exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	if code := runGw("gateway", "add", "gw-key", "--provider", "openrouter", "--api-key", "sk-flag"); code != int(anyerr.Success) {
		t.Fatalf("--api-key add exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	t.Setenv("GW_TEST_KEY", "sk-from-env-var")
	if code := runGw("gateway", "add", "gw-var", "--provider", "openrouter", "--api-key-env", "GW_TEST_KEY"); code != int(anyerr.Success) {
		t.Fatalf("--api-key-env add exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	if code := runGw("gateway", "add", "gw-models", "--provider", "openrouter",
		"--secret", "sk-x", "--model", "m-default", "--models", "default=m1", "--models", "sonnet=m2"); code != int(anyerr.Success) {
		t.Fatalf("--model/--models add exited %d (stdout=%q stderr=%q)", code, lastStdout, lastStderr)
	}
	def, models := getProfile("gw-models")
	if def != "m-default" || models["default"] != "m1" || models["sonnet"] != "m2" {
		t.Fatalf("model roles not persisted: default=%q models=%v", def, models)
	}

	// Exactly one key source.
	t.Setenv("ANYPICK_SECRET", "")
	if code := runGw("gateway", "add", "gw-two", "--provider", "openrouter",
		"--secret", "a", "--api-key", "b"); code != int(anyerr.InvalidUsage) {
		t.Fatalf("two key sources exited %d, want InvalidUsage", code)
	}
	if code := runGw("gateway", "add", "gw-missing", "--provider", "openrouter",
		"--api-key-env", "GW_DEFINITELY_UNSET_VAR"); code != int(anyerr.InvalidUsage) {
		t.Fatalf("unset --api-key-env exited %d, want InvalidUsage", code)
	}
	if code := runGw("gateway", "add", "gw-badmodels", "--provider", "openrouter",
		"--secret", "sk-x", "--models", "not-a-role-mapping"); code != int(anyerr.InvalidUsage) {
		t.Fatalf("malformed --models exited %d, want InvalidUsage", code)
	}
	// Bare --api-key prompts, which refuses without a TTY.
	if !isTTY(os.Stdin) {
		if code := runGw("gateway", "add", "gw-prompt", "--provider", "openrouter", "--api-key"); code != int(anyerr.InvalidUsage) {
			t.Fatalf("bare --api-key without TTY exited %d, want InvalidUsage", code)
		}
	}
}
