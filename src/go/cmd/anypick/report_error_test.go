package main

// Exception-reporting contract at the CLI boundary. These pin the two things
// the presentation layer used to drop: the coded exit status and the
// suggestions that live on the *anyerr.Error instance.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestReportErrUsesTheCodedExitStatus(t *testing.T) {
	defer resetGlobals()()
	captureBoth(t, func() {
		err := anyerr.Newf("No account named work.", "ACCOUNT_NOT_FOUND",
			anyerr.WithExitCode(anyerr.NotFound),
			anyerr.WithSuggestions("Run `anypick list accounts`."))
		if code := reportErr(err); code != int(anyerr.NotFound) {
			t.Fatalf("reportErr = %d, want %d", code, int(anyerr.NotFound))
		}
	})
	if !strings.Contains(lastStderr, "No account named work.") {
		t.Fatalf("stderr = %q", lastStderr)
	}
	if !strings.Contains(lastStderr, "→ Run `anypick list accounts`.") {
		t.Fatalf("suggestions must reach the terminal, stderr = %q", lastStderr)
	}
}

func TestReportErrKeepsStdoutMachinePure(t *testing.T) {
	// A consumer under --json reads stdout: it must see either one success
	// document or nothing, and learn what happened from the exit code
	// (dryrun_gate_test.go pins the same rule for the confirmation gate).
	defer resetGlobals()()
	globals.JSON = true
	captureBoth(t, func() {
		reportErr(anyerr.Newf("Could not start the proxy.", "PROXY_START_FAILED"))
	})
	if strings.TrimSpace(lastStdout) != "" {
		t.Fatalf("stdout = %q, want empty on the error path", lastStdout)
	}
	if !strings.Contains(lastStderr, "Could not start the proxy.") {
		t.Fatalf("stderr = %q", lastStderr)
	}
}

func TestReportErrOnAPlainErrorIsOperational(t *testing.T) {
	defer resetGlobals()()
	captureBoth(t, func() {
		if code := reportErr(errors.New("boom")); code != int(anyerr.Operational) {
			t.Fatalf("reportErr = %d, want %d", code, int(anyerr.Operational))
		}
	})
	if !strings.Contains(lastStderr, "boom") {
		t.Fatalf("stderr = %q", lastStderr)
	}
}

func TestReportErrOnNilSucceeds(t *testing.T) {
	// reportErr is called from paths that may carry a nil error; treating that
	// as a failure would fail commands that worked.
	defer resetGlobals()()
	if code := reportErr(nil); code != int(anyerr.Success) {
		t.Fatalf("reportErr(nil) = %d, want 0", code)
	}
}

// TestStartupFailureKeepsItsExitCode is the reason run() routes through
// reportErr: app.Ready failures are already coded (an unreadable data
// directory, a newer schema), and flattening them all to exit 1 hid the
// documented ranges from scripts and from CI.
func TestStartupFailureKeepsItsExitCode(t *testing.T) {
	defer resetGlobals()()

	startup := anyerr.Newf("The data directory cannot be read.", "SCHEMA_NEWER",
		anyerr.WithExitCode(anyerr.HealthFailure),
		anyerr.WithSuggestions("Update AnyPick to match this data directory."))
	captureBoth(t, func() {
		if code := reportErr(startup); code != int(anyerr.HealthFailure) {
			t.Fatalf("startup failure exit = %d, want %d", code, int(anyerr.HealthFailure))
		}
	})
	if !strings.Contains(lastStderr, "Update AnyPick") {
		t.Fatalf("startup suggestions must reach the terminal, stderr = %q", lastStderr)
	}
}

func TestReportErrEmitsStructuredStderrUnderJSON(t *testing.T) {
	// A JSON-mode caller must not have to scrape prose for the code and the
	// hints, and stdout must stay reserved for command output.
	defer resetGlobals()()
	globals.JSON = true
	captureBoth(t, func() {
		reportErr(anyerr.Newf("No account named work.", "NOT_FOUND",
			anyerr.WithSuggestions("Run `anypick list accounts`.")))
	})
	if strings.TrimSpace(lastStdout) != "" {
		t.Fatalf("stdout = %q, want empty", lastStdout)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(lastStderr)), &payload); err != nil {
		t.Fatalf("stderr is not one JSON value: %q (%v)", lastStderr, err)
	}
	if payload["ok"] != false {
		t.Fatalf("ok = %v, want false", payload["ok"])
	}
	if payload["code"] != "NOT_FOUND" {
		t.Fatalf("code = %v", payload["code"])
	}
	if payload["exitCode"] != float64(anyerr.NotFound) {
		t.Fatalf("exitCode = %v, want %d", payload["exitCode"], int(anyerr.NotFound))
	}
	suggestions, _ := payload["suggestions"].([]any)
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %v", payload["suggestions"])
	}
}

func TestReportErrLeavesUnreservedCodesOperational(t *testing.T) {
	// Only the reserved families carry a documented range (see anyerr): a
	// provider-specific code such as ACCOUNT_NOT_FOUND is a plain failure, and
	// this test exists so widening the mapping stays a deliberate act.
	defer resetGlobals()()
	captureBoth(t, func() {
		if code := reportErr(anyerr.Newf("gone", "ACCOUNT_NOT_FOUND")); code != int(anyerr.Operational) {
			t.Fatalf("exit = %d, want %d", code, int(anyerr.Operational))
		}
	})
}
