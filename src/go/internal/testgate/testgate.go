// Package testgate holds the shared AST gates that keep table-owning
// packages honest. Each owner (store, journal, hub, pools, presets, profile,
// bindings, …) gets a small ctxfirst_test.go that calls into here instead of
// carrying its own copy of the check — five near-identical copies already
// drifted once, which is exactly what a gate must not do.
package testgate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// mutationVerbs reports whether a method name reads as a persisted mutation.
// The list is intentionally broad: a false positive costs one allowlist entry
// with a sentence of justification, while a false negative costs silent
// corruption or a coordinator deadlock.
func mutationVerbs(name string) bool {
	for _, verb := range []string{
		"Write", "Set", "Clear", "Delete", "Patch", "Update", "Create", "Remove",
		"Save", "Upsert", "Insert", "Discard", "Prepare", "Ingest", "Reset",
		"Rotate", "Revoke", "Mark", "Move", "Replace", "Apply", "Attach", "Detach",
		"Migrate", "Restore",
	} {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

func isContextType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Context" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "context"
}

// CheckNoCtxlessMutations fails when an exported method in dir whose name
// reads as a mutation does not take context.Context as its first parameter.
// A mutating wrapper that starts from context.Background() cannot see a
// coordinator lock its caller holds, so it re-acquires and blocks forever on
// the in-process mutex — the process hangs instead of failing. Reads keep
// their ctx-less form because they take no lock.
//
// allowed maps an exempt method to the written reason it is safe; an empty
// reason fails, because adding to the map is a decision, not a workaround.
func CheckNoCtxlessMutations(t *testing.T, dir string, allowed map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		files = append(files, f)
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Type.Params == nil {
				continue
			}
			name := fn.Name.Name
			if !ast.IsExported(name) || !mutationVerbs(name) {
				continue
			}
			if reason, ok := allowed[name]; ok {
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s is allowlisted with no reason; say why it is safe", name)
				}
				continue
			}
			params := fn.Type.Params.List
			if len(params) == 0 || !isContextType(params[0].Type) {
				t.Errorf("mutating method %s must take context.Context first; add a Context twin and delete this form", name)
			}
		}
	}
}
