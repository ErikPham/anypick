// Package testgate holds the shared AST gates that keep table-owning
// packages honest. Each owner (store, journal, hub, pools, presets, profile,
// bindings, …) gets a small ctxfirst_test.go that calls into here instead of
// carrying its own copy of the check — five near-identical copies already
// drifted once, which is exactly what a gate must not do.
package testgate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mutationVerbs reports whether a method name reads as a persisted mutation.
// The list is intentionally broad: a false positive costs one allowlist entry
// with a sentence of justification, while a false negative costs silent
// corruption or a coordinator deadlock.
//
// "Record" is here because bindings.RecordSourceResumeContext writes the
// source_resumes row. It is the one verb this list gained from a copy rather
// than from the original: bindings carried its own drifted copy of this check
// (missing Attach, Detach, Migrate and Restore), and merging the two had to
// keep both sides' coverage rather than the shared list's.
func mutationVerbs(name string) bool {
	for _, verb := range []string{
		"Write", "Set", "Clear", "Delete", "Patch", "Update", "Create", "Remove",
		"Save", "Upsert", "Insert", "Discard", "Prepare", "Ingest", "Reset",
		"Rotate", "Revoke", "Mark", "Move", "Replace", "Apply", "Attach", "Detach",
		"Migrate", "Restore", "Record",
	} {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

// PackageFiles parses dir's non-test sources. Test files are excluded: a test
// that starts from a background context is not inside a held lock.
//
// It is exported so a package can add its own AST rule on the same parse
// instead of carrying a second walker — bindings keeps a
// no-context.Background() guard alongside the shared ctx-first one.
func PackageFiles(dir string, fset *token.FileSet) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read package dir: %w", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Rooted at dir, like the ReadDir above — see the note in
		// CtxlessMutations for why a bare name is a defect.
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", name, perr)
		}
		files = append(files, f)
	}
	return files, nil
}

func isContextType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Context" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "context"
}

// Finding is one method that reads as a persisted mutation and does not take
// context.Context first.
type Finding struct {
	// Method is the offending method name.
	Method string
	// Detail is the message a caller reports verbatim.
	Detail string
}

// CtxlessMutations reports the exported methods in dir whose name reads as a
// mutation and which do not take context.Context as their first parameter.
//
// Detection is separated from reporting deliberately, and for the reason this
// package exists at all: a gate nobody can falsify is a gate nobody has proven.
// Six packages call this check, so a defect here makes six gates go quiet at
// once — and a check welded to *testing.T cannot itself be tested. Detection
// returns findings; CheckNoCtxlessMutations only prints them.
//
// allowed maps an exempt method to the written reason it is safe; an empty
// reason is itself a finding, because adding to the map is a decision, not a
// workaround.
func CtxlessMutations(dir string, allowed map[string]string) ([]Finding, error) {
	fset := token.NewFileSet()
	files, err := PackageFiles(dir, fset)
	if err != nil {
		return nil, err
	}
	var findings []Finding
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
					findings = append(findings, Finding{
						Method: name,
						Detail: name + " is allowlisted with no reason; say why it is safe",
					})
				}
				continue
			}
			params := fn.Type.Params.List
			if len(params) == 0 || !isContextType(params[0].Type) {
				findings = append(findings, Finding{
					Method: name,
					Detail: "mutating method " + name + " must take context.Context first; " +
						"add a Context twin and delete this form",
				})
			}
		}
	}
	return findings, nil
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
	findings, err := CtxlessMutations(dir, allowed)
	if err != nil {
		t.Fatalf("ctxfirst gate could not run: %v", err)
	}
	for _, finding := range findings {
		t.Errorf("%s", finding.Detail)
	}
}
