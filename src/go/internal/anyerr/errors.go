// Package anyerr ports src/utils/errors.ts: exit codes and the structured
// AnyPickError carried across the core layer.
package anyerr

import (
	"errors"
	"fmt"
)

// ExitCode mirrors the reserved ranges in src/utils/errors.ts (§25.2).
type ExitCode int

const (
	Success            ExitCode = 0
	Operational        ExitCode = 1
	InvalidUsage       ExitCode = 2
	NotFound           ExitCode = 3
	AuthRequired       ExitCode = 4
	CapabilityConflict ExitCode = 5
	HealthFailure      ExitCode = 6
	MissingDependency  ExitCode = 7
	ImportFormat       ExitCode = 8
	ImportLimit        ExitCode = 9
	Cancelled          ExitCode = 130 // 128 + SIGINT
)

// Error is the Go equivalent of AnyPickError. Code is a stable machine string
// (e.g. INVALID_REFERENCE); ExitCode drives process termination; Suggestions
// are printed by the presentation layer only.
type Error struct {
	Code        string
	Message     string
	ExitCode    ExitCode
	Suggestions []string
	Mutated     bool
	Details     map[string]any
	cause       error
}

func (e *Error) Error() string { return e.Message }

// Unwrap exposes the wrapped cause so errors.As / errors.Is traverse the chain.
func (e *Error) Unwrap() error { return e.cause }

// reservedExitCodes maps the reserved code families to the exit status the CLI
// documents for them (docs/src/content/docs/reference/cli.mdx §Exit codes).
//
// The mapping lives here, not at the call sites, because a code and an exit
// status that can disagree silently will: twenty call sites already carried an
// `INVALID_USAGE` that exited 1, an `IMPORT_FORMAT` that exited 1, and a
// `HEALTH_FAILURE` that exited 1, so a script branching on the table the docs
// publish got the wrong branch. Deriving the status from the code makes the
// documented contract true by construction; WithExitCode still overrides it
// where a command genuinely means something else.
var reservedExitCodes = map[string]ExitCode{
	"INVALID_USAGE":       InvalidUsage,
	"NOT_FOUND":           NotFound,
	"AUTH_REQUIRED":       AuthRequired,
	"CAPABILITY_CONFLICT": CapabilityConflict,
	"HEALTH_FAILURE":      HealthFailure,
	"MISSING_DEPENDENCY":  MissingDependency,
	"IMPORT_FORMAT":       ImportFormat,
	"IMPORT_LIMIT":        ImportLimit,
	"CANCELLED":           Cancelled,
}

// ExitCodeForCode reports the documented status for a reserved code, and
// Operational for every other code — an unrecognised code is a plain failure,
// not a success.
func ExitCodeForCode(code string) ExitCode {
	if reserved, ok := reservedExitCodes[code]; ok {
		return reserved
	}
	return Operational
}

func New(message, code string) *Error {
	return &Error{Message: message, Code: code, ExitCode: ExitCodeForCode(code)}
}

type Option func(*Error)

func WithExitCode(c ExitCode) Option { return func(e *Error) { e.ExitCode = c } }
func WithMutated(m bool) Option      { return func(e *Error) { e.Mutated = m } }
func WithSuggestions(s ...string) Option {
	return func(e *Error) { e.Suggestions = append(e.Suggestions, s...) }
}
func WithDetails(d map[string]any) Option { return func(e *Error) { e.Details = d } }

func Newf(message, code string, opts ...Option) *Error {
	e := New(message, code)
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// ExitCodeOf reports the process exit code for an arbitrary error value.
func ExitCodeOf(err error) ExitCode {
	var ae *Error
	if errors.As(err, &ae) && ae != nil {
		return ae.ExitCode
	}
	return Operational
}

// WithCause attaches a wrapped cause for errors.As / errors.Is traversal.
func WithCause(cause error) Option { return func(e *Error) { e.cause = cause } }

// Wrapf annotates an error with an AnyPick code while preserving the original
// message for debugging and chaining the cause. ExitCode, Suggestions, and
// Mutated are copied from an inner *Error so reportErr still prints hints
// (invariant 7) unless an explicit Option overrides them.
func Wrapf(err error, message, code string, opts ...Option) *Error {
	msg := message
	if err != nil {
		msg = fmt.Sprintf("%s: %v", message, err)
	}
	e := New(msg, code)
	e.cause = err
	var inner *Error
	if errors.As(err, &inner) && inner != nil {
		e.ExitCode = inner.ExitCode
		e.Mutated = inner.Mutated
		if len(inner.Suggestions) > 0 {
			e.Suggestions = append([]string(nil), inner.Suggestions...)
		}
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}
