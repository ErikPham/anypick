package desktop

import (
	"context"
	"errors"
	"fmt"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// The engine already knows why an operation failed: anyerr carries a stable
// code, the suggestions the CLI prints, and — because only the executor knows
// whether it wrote before it failed — the Mutated flag (invariant 3). The
// desktop boundary used to flatten all of that into err.Error(), so the app
// could only show one sentence and could never tell a retryable failure from
// one that needs `doctor`. Fail is the single place that keeps the structure
// while still refusing to render anything that looks like a credential
// (display_safety.go).

// maxResultSuggestions bounds the advice an app failure can carry. The CLI
// prints every suggestion to a terminal a human reads; the app renders them in
// a fixed-height banner, and an unbounded list would push the dismiss action
// off-screen.
const maxResultSuggestions = 3

// Fail returns result marked as a terminal failure with the structured detail
// the native client needs to explain it.
//
// It is idempotent for a nil error: Fail(nil) leaves result untouched, which
// keeps `if err != nil { result = result.Fail(err) }` as the only shape a
// caller needs.
func (r Result) Fail(err error) Result {
	if err == nil {
		return r
	}
	r.Status = "error"

	var ae *anyerr.Error
	if errors.As(err, &ae) && ae != nil {
		r.Code = ae.Code
		r.Mutated = ae.Mutated
		r.Suggestions = displaySafeSuggestions(ae.Suggestions)
	}

	message := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// We cannot know whether the executor finished or rolled back;
		// claiming settings were kept is a lie the user may act on.
		message = "The action timed out before it finished. Check Live Logs and run doctor before retrying."
	case errors.Is(err, context.Canceled):
		message = "The action was cancelled before it finished. Check Live Logs before retrying."
	}

	if r.Mutated {
		// Sanitize first so the warning suffix cannot be dropped wholesale by
		// the length/opaque check.
		r.Message = displaySafeMessageWithNotice(message, " (state was partially changed — run doctor)")
	} else {
		r.Message = displaySafeMessage(message)
	}
	if r.Message == "" {
		r.Message = "The operation failed without a reason the engine could report."
	}
	return r
}

// Cancel marks a result as cancelled rather than failed: the user asked for it,
// so it must not raise the app's error affordances.
func (r Result) Cancel(message string) Result {
	r.Status = "cancelled"
	r.Message = displaySafeMessage(message)
	r.Code = ""
	r.Suggestions = nil
	return r
}

// payloadErr is the desktop counterpart of CLI exit code 2. Naming the field
// the client left out is the whole message: there is no user action beyond
// fixing the request, and the code lets the app stop treating it as a
// service failure it should retry.
func payloadErr(operation, need string) error {
	return anyerr.Newf(operation+" needs "+need, "INVALID_USAGE",
		anyerr.WithExitCode(anyerr.InvalidUsage))
}

// panicErr is the error a recovered mutation panic reports to the app.
//
// It always claims Mutated: a handler that died mid-flight may have written to
// a credential file and then failed to record it, and telling the user nothing
// changed would be a lie they could act on (ADR 0004, invariant 3).
func panicErr(cause any) error {
	return anyerr.Newf(
		"The engine hit an internal error running that action.",
		"INTERNAL_PANIC",
		anyerr.WithMutated(true),
		anyerr.WithSuggestions(
			"Run `anypick doctor` before retrying.",
			"Report this with the engine log if it repeats."),
		anyerr.WithCause(fmt.Errorf("panic: %v", cause)))
}

// actionTimeoutErr reports an action that ran past its budget without
// answering. The host must get a terminal frame either way: a row that waits
// forever is worse than one that failed, and a mutation that may have written
// must say so.
func actionTimeoutErr(operation string, mutated bool) error {
	opts := []anyerr.Option{
		anyerr.WithSuggestions(
			"Please try the action again in a moment.",
			"Check Live Logs in the Activity pane for details.",
			"If it repeats, run `anypick doctor` or restart AnyPick."),
	}
	if mutated {
		opts = append(opts, anyerr.WithMutated(true))
	}
	return anyerr.Newf(
		fmt.Sprintf("The engine ran out of time running %s.", operation),
		"ACTION_TIMEOUT", opts...)
}

// loginTimeoutErr reports a sign-in that outlived the engine's own backstop.
// The browser launcher bounds itself, so this fires only for an adapter that
// never returns — the host must still hear about it.
func loginTimeoutErr(providerID string) error {
	return anyerr.Newf(
		"The "+providerID+" sign-in did not finish in time.",
		"LOGIN_TIMEOUT",
		anyerr.WithSuggestions(
			"Cancel the sign-in and try again.",
			"If a login was saved anyway, remove the duplicate in Accounts & APIs."))
}

// serviceUnavailableErr reports a service the composition did not assemble.
// `service` is the noun phrase the user should read, without the adjective:
// "config service" becomes "config service unavailable". It is always a
// packaging or startup fault, never something the user typed, and the code and
// advice travel together so the seven call sites cannot drift apart.
func serviceUnavailableErr(service string) error {
	return anyerr.Newf(service+" unavailable", "SERVICE_UNAVAILABLE",
		anyerr.WithSuggestions("Restart AnyPick. If the problem persists, verify the background service status."))
}

// displaySafeSuggestions drops advice that cannot be rendered safely and caps
// the list. A suggestion is a hint, so losing one is survivable — unlike
// losing the sanitization guarantee, which is why each one is checked with the
// same rule as the message.
func displaySafeSuggestions(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(raw), maxResultSuggestions))
	for _, suggestion := range raw {
		if value := displaySafeMessage(suggestion); value != "" && value != unsafeDisplayFallback {
			out = append(out, value)
		}
		if len(out) == maxResultSuggestions {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
