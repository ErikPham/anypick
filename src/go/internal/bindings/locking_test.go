package bindings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
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
func TestMutatingMethodsTakeContext(t *testing.T) {
	fset := token.NewFileSet()
	files, err := packageFiles(fset)
	if err != nil {
		t.Fatalf("parse the bindings package: %v", err)
	}

	var missing []string
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if !ast.IsExported(name) || !mutatesByVerbs(name) {
				continue
			}
			if !takesContextFirst(fn) {
				missing = append(missing, name+" ("+fset.Position(fn.Pos()).String()+")")
			}
		}
	}

	if len(missing) > 0 {
		t.Errorf("mutating methods that do not take the caller's context (ADR 0009): %s\n"+
			"Give the method a ctx context.Context first parameter. A ctx-less twin\n"+
			"that starts from context.Background() cannot see a lock the caller holds.",
			strings.Join(missing, "; "))
	}
}

// TestNoBackgroundContextInProductionCode catches the other half of the same
// trap: a product path that manufactures a fresh context inside a region that
// may already be locked.
func TestNoBackgroundContextInProductionCode(t *testing.T) {
	fset := token.NewFileSet()
	files, err := packageFiles(fset)
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

// packageFiles parses the package's non-test sources. Test files are excluded:
// a test that starts from a background context is not inside a held lock.
func packageFiles(fset *token.FileSet) ([]*ast.File, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			return nil, perr
		}
		files = append(files, f)
	}
	return files, nil
}

// mutatesByVerbs reports whether a method name reads as a mutation. Broad on
// purpose: a false positive costs a sentence of justification, and a false
// negative costs a hang. "Record" is included because RecordSourceResume writes
// the source_resumes row.
func mutatesByVerbs(name string) bool {
	for _, verb := range []string{
		"Write", "Set", "Clear", "Delete", "Patch", "Update", "Create", "Remove",
		"Save", "Upsert", "Insert", "Discard", "Prepare", "Ingest", "Reset",
		"Rotate", "Revoke", "Mark", "Move", "Replace", "Apply", "Record",
	} {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

// takesContextFirst reports whether the method's first parameter is a
// context.Context.
func takesContextFirst(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return false
	}
	sel, ok := fn.Type.Params.List[0].Type.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Context" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "context"
}
