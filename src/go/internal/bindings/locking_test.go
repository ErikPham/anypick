package bindings

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// These two guards exist because of a shape this package used to have: every
// mutator had a ctx-less twin that called the real method with
// context.Background().
//
// That is the re-entrancy trap of ADR 0009. A lock the caller already holds is
// carried *in the context*, so a mutator reached through a fresh
// context.Background() cannot see it, calls AcquireFileLock again, and blocks
// on the in-process mutex forever — the process hangs instead of failing.
// Today bindings takes no lock of its own, so the ctx-less forms were merely a
// landmine rather than a live deadlock; the moment bindings acquires a
// coordinator lock, every one of them becomes a hang.
//
// internal/store solved this by deleting its ctx-less mutators and letting the
// compiler refuse the mistake. This package does the same, and these tests stop
// anyone re-adding them.

// TestMutatingMethodsTakeContext enforces invariant #5's contract for this
// package: a mutating method must receive the caller's context, so a lock held
// upstream stays visible to it.
//
// The check itself lives in internal/testgate, beside the six other packages
// that own persisted state. This package used to carry its own copy, and that
// copy had drifted: it was missing Attach, Detach, Migrate and Restore, so a
// ctx-less AttachProject or RestoreSnapshot would have passed the guard. That
// is precisely the failure the shared implementation exists to prevent — "five
// near-identical copies already drifted once, which is exactly what a gate must
// not do" — so this is now a call rather than a seventh implementation.
func TestMutatingMethodsTakeContext(t *testing.T) {
	testgate.CheckNoCtxlessMutations(t, ".", map[string]string{
		// No exceptions: every bindings mutation is ctx-first.
	})
}

// TestNoBackgroundContextInProductionCode catches the other half of the same
// trap: a product path that manufactures a fresh context inside a region that
// may already be locked.
//
// The rule is bindings-specific, so it stays here; only the parse is shared.
func TestNoBackgroundContextInProductionCode(t *testing.T) {
	fset := token.NewFileSet()
	files, err := testgate.PackageFiles(".", fset)
	if err != nil {
		t.Fatalf("parse the bindings package: %v", err)
	}

	var offenders []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Background" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "context" {
				return true
			}
			offenders = append(offenders, fset.Position(call.Pos()).String())
			return true
		})
	}

	if len(offenders) > 0 {
		t.Errorf("context.Background() in bindings production code at %s\n"+
			"A fresh context cannot see a lock the caller holds, so a mutator reached\n"+
			"this way re-enters the coordinator and hangs (ADR 0009). Thread the\n"+
			"caller's ctx through instead.",
			strings.Join(offenders, ", "))
	}
}
