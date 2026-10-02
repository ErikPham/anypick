package anyerr

import (
	"errors"
	"testing"
)

func TestWrapfChainsCause(t *testing.T) {
	cause := errors.New("disk full")
	wrapped := Wrapf(cause, "write failed", "IO_WRITE")
	if !errors.Is(wrapped, cause) {
		t.Fatalf("errors.Is: want true for wrapped cause")
	}
	var ae *Error
	if !errors.As(wrapped, &ae) {
		t.Fatalf("errors.As: want *Error")
	}
	if ae.Code != "IO_WRITE" {
		t.Fatalf("Code=%q want IO_WRITE", ae.Code)
	}
}

func TestExitCodeOf(t *testing.T) {
	if got := ExitCodeOf(Newf("bad usage", "BAD", WithExitCode(InvalidUsage))); got != InvalidUsage {
		t.Fatalf("ExitCodeOf=%v want InvalidUsage", got)
	}
	if got := ExitCodeOf(Wrapf(errors.New("x"), "y", "Z", WithExitCode(NotFound))); got != NotFound {
		t.Fatalf("ExitCodeOf wrapped=%v want NotFound", got)
	}
	if got := ExitCodeOf(errors.New("plain")); got != Operational {
		t.Fatalf("ExitCodeOf plain=%v want Operational", got)
	}
}

func TestWrapfPreservesInnerExitCodeAndSuggestions(t *testing.T) {
	inner := Newf("missing", "NOT_FOUND",
		WithExitCode(NotFound),
		WithSuggestions("run login"))
	wrapped := Wrapf(inner, "list accounts", "ACCOUNT_LIST_FAILED")
	if wrapped.ExitCode != NotFound {
		t.Fatalf("ExitCode=%v want NotFound", wrapped.ExitCode)
	}
	if len(wrapped.Suggestions) != 1 || wrapped.Suggestions[0] != "run login" {
		t.Fatalf("Suggestions=%v", wrapped.Suggestions)
	}
	over := Wrapf(inner, "list accounts", "ACCOUNT_LIST_FAILED", WithExitCode(Operational))
	if over.ExitCode != Operational {
		t.Fatalf("explicit option must win: %v", over.ExitCode)
	}
}

func TestWrapfNilCause(t *testing.T) {
	wrapped := Wrapf(nil, "oops", "SOME_CODE")
	if wrapped.cause != nil {
		t.Fatalf("cause should be nil when Wrapf(nil, …)")
	}
	if wrapped.Message != "oops" {
		t.Fatalf("Message=%q want oops", wrapped.Message)
	}
}

// ---- reserved code ↔ exit status ----

// TestReservedCodesCarryTheirDocumentedExitStatus is the gate for the table in
// docs/src/content/docs/reference/cli.mdx §Exit codes. A code and an exit
// status that can drift silently will: twenty call sites already shipped an
// INVALID_USAGE that exited 1 and an IMPORT_FORMAT that exited 1, so a script
// branching on the documented table took the wrong branch.
func TestReservedCodesCarryTheirDocumentedExitStatus(t *testing.T) {
	cases := []struct {
		code string
		want ExitCode
	}{
		{"INVALID_USAGE", InvalidUsage},
		{"NOT_FOUND", NotFound},
		{"AUTH_REQUIRED", AuthRequired},
		{"CAPABILITY_CONFLICT", CapabilityConflict},
		{"HEALTH_FAILURE", HealthFailure},
		{"MISSING_DEPENDENCY", MissingDependency},
		{"IMPORT_FORMAT", ImportFormat},
		{"IMPORT_LIMIT", ImportLimit},
		{"CANCELLED", Cancelled},
	}
	for _, tc := range cases {
		if got := New("m", tc.code).ExitCode; got != tc.want {
			t.Errorf("New(code=%q).ExitCode = %d, want %d", tc.code, got, tc.want)
		}
		if got := Newf("m", tc.code).ExitCode; got != tc.want {
			t.Errorf("Newf(code=%q).ExitCode = %d, want %d", tc.code, got, tc.want)
		}
		if got := ExitCodeOf(Wrapf(errors.New("cause"), "m", tc.code)); got != tc.want {
			t.Errorf("Wrapf(plain cause, code=%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
}

func TestUnreservedCodeIsAnOperationalFailure(t *testing.T) {
	// An unrecognised code must not read as success: exit 0 means "it worked".
	for _, code := range []string{"SCHEMA_NEWER", "INTERNAL_PANIC", "", "nothing-like-this"} {
		if got := ExitCodeForCode(code); got != Operational {
			t.Errorf("ExitCodeForCode(%q) = %d, want %d", code, got, Operational)
		}
		if got := New("m", code).ExitCode; got != Operational {
			t.Errorf("New(code=%q).ExitCode = %d, want %d", code, got, Operational)
		}
	}
}

func TestExplicitExitCodeOverridesTheReservedDefault(t *testing.T) {
	// The mapping is a default, not a law: a command that genuinely means
	// something else must still be able to say so.
	e := Newf("m", "NOT_FOUND", WithExitCode(Operational))
	if e.ExitCode != Operational {
		t.Fatalf("ExitCode = %d, want %d", e.ExitCode, Operational)
	}
}

func TestErrorOptionsAndMethods(t *testing.T) {
	e := New("msg", "CODE")
	if e.Error() != "msg" || e.Code != "CODE" || e.ExitCode != Operational {
		t.Fatalf("New = %#v", e)
	}
	// WithMutated / WithDetails / WithSuggestions / WithCause
	e2 := Newf("x", "Y", WithMutated(true), WithDetails(map[string]any{"k": 1}), WithSuggestions("try this", "or that"), WithCause(errors.New("root")))
	if !e2.Mutated || e2.Details["k"] != 1 || len(e2.Suggestions) != 2 {
		t.Fatalf("options not applied: %#v", e2)
	}
	if e2.Unwrap() == nil || e2.Unwrap().Error() != "root" {
		t.Fatalf("Unwrap=%v want root", e2.Unwrap())
	}
	// WithCause nil is allowed.
	e3 := Newf("a", "B", WithCause(nil))
	if e3.Unwrap() != nil {
		t.Fatalf("WithCause(nil) should leave nil cause")
	}
	// Unwrap on plain Error with no cause returns nil.
	if New("m", "C").Unwrap() != nil {
		t.Fatalf("Unwrap want nil for no-cause error")
	}
}
