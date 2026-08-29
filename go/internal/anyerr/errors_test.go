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

func TestWrapfNilCause(t *testing.T) {
	wrapped := Wrapf(nil, "oops", "SOME_CODE")
	if wrapped.cause != nil {
		t.Fatalf("cause should be nil when Wrapf(nil, …)")
	}
	if wrapped.Message != "oops" {
		t.Fatalf("Message=%q want oops", wrapped.Message)
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
