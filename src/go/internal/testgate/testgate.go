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

// MutationVerbs reports whether a method name reads as a persisted mutation.
// The list is intentionally broad: a false positive costs one allowlist entry
// with a sentence of justification, while a false negative costs silent
// corruption or a coordinator deadlock.
//
// "Record" is here because bindings.RecordSourceResumeContext writes the
// source_resumes row. It is the one verb this list gained from a copy rather
// than from the original: bindings carried its own drifted copy of this check
// (missing Attach, Detach, Migrate and Restore), and merging the two had to
// keep both sides' coverage rather than the shared list's.
// MutationVerbs is the detection's entire signal, so it is exported rather than
// mirrored: a second copy in a package that gates the coordinator went quiet
// while looking green. Every gate that decides "is this a mutation" must read
// this list, so a newly added verb cannot reach one gate and miss another.
func MutationVerbs(name string) bool {
	for _, verb := range []string{
		"Write", "Set", "Clear", "Delete", "Patch", "Update", "Create", "Remove",
		"Save", "Upsert", "Insert", "Discard", "Prepare", "Ingest", "Reset",
		"Rotate", "Revoke", "Mark", "Move", "Replace", "Apply", "Attach", "Detach",
		"Migrate", "Restore", "Record",
		// ReleaseProxyRuntimeContext and CopySnapshotFiles are persisted
		// mutations that self-lock today, but the gate never looked at a name
		// starting with these verbs, so deleting their lock would have passed.
		"Release", "Copy", "Heartbeat", "Reap",
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
// allowed maps an exempt method to the written reason it is safe, keyed by
// "Type.Method": the receiver type name as ReceiverName reports it (so a
// pointer and a value receiver share one key), a dot, then the method name.
//
// The type belongs in the key because these verbs are common method names.
// Keying on the method alone let an http.ResponseWriter exemption extend
// silently to every other type in the package, which is how a comment came to
// name four implementations while the code exempted all of them. One directory
// is one package and Go forbids two types with one name in a package, so
// "Type.Method" is unambiguous without disambiguating pointer from value.
//
// An empty reason is itself a finding, because adding to the map is a decision,
// not a workaround.
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
			if !ast.IsExported(name) || !MutationVerbs(name) {
				continue
			}
			key := ReceiverName(fn) + "." + name
			if reason, ok := allowed[key]; ok {
				if strings.TrimSpace(reason) == "" {
					findings = append(findings, Finding{
						Method: key,
						Detail: key + " is allowlisted with no reason; say why it is safe",
					})
				}
				continue
			}
			params := fn.Type.Params.List
			if len(params) == 0 || !isContextType(params[0].Type) {
				findings = append(findings, Finding{
					Method: key,
					Detail: "mutating method " + key + " must take context.Context first; " +
						"add a Context twin and delete this form",
				})
			}
		}
	}
	return findings, nil
}

// A mutating wrapper that starts from context.Background() cannot see a
// coordinator lock its caller holds, so it re-acquires and waits on the
// in-process mutex until the budget runs out, then fails with
// MUTATION_LOCK_TIMEOUT. Reads keep their ctx-less form because they take no
// lock.
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

// TakesCoordinatorLock reports whether a method body routes through the
// mutation coordinator. A store's own mutate wrapper counts, as does the
// WithMutationLock family, which a mutation spanning several provider scopes
// uses directly.
//
// One implementation, shared: two stores each keeping their own copy is how a
// verb or a wrapper added to one gate quietly missed the other, which is the
// whole reason this package exists.
func TakesCoordinatorLock(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			switch fn.Sel.Name {
			case "mutate", "WithMutationLock", "WithMutationLocks":
				found = true
			}
		case *ast.Ident:
			switch fn.Name {
			case "WithMutationLock", "WithMutationLocks":
				found = true
			}
		}
		return !found
	})
	return found
}

// ReceiverName is the bare type name a method is declared on, with any pointer
// marker dropped: both `LeaseStore` and `*LeaseStore` report "LeaseStore".
func ReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// UncoordinatedMutations reports the exported methods in dir whose name reads
// as a persisted mutation and whose body never routes through the coordinator.
// It is the second half of the same rule CtxlessMutations guards from the other
// side: ctx-first is what lets a mutation see the caller's lock, and the lock
// is what makes two actors on one provider serialise.
//
// receivers narrows the sweep to named types. Pass nil for every receiver.
// Narrowing matters because the rule is about the type that owns persisted
// state: a service method reaches the database through a store's ctx-first
// mutator, so it is the store that carries the obligation, and gating the
// service package too would only grow an exemption list (CODE_REVIEW.md §8,
// health signals).
//
// allowed maps an exempt method to the written reason it is safe, keyed the
// same way CtxlessMutations keys its map: "Type.Method". An empty reason is
// itself a finding, for the same reason it is one there.
func UncoordinatedMutations(dir string, receivers []string, allowed map[string]string) ([]Finding, error) {
	fset := token.NewFileSet()
	files, err := PackageFiles(dir, fset)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(receivers))
	for _, r := range receivers {
		wanted[r] = true
	}
	var findings []Finding
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if !ast.IsExported(name) || !MutationVerbs(name) {
				continue
			}
			recv := ReceiverName(fn)
			if len(wanted) > 0 && !wanted[recv] {
				continue
			}
			key := recv + "." + name
			if reason, ok := allowed[key]; ok {
				if strings.TrimSpace(reason) == "" {
					findings = append(findings, Finding{
						Method: key,
						Detail: key + " is allowlisted with no reason; say why it is safe",
					})
				}
				continue
			}
			if !TakesCoordinatorLock(fn.Body) {
				findings = append(findings, Finding{
					Method: key,
					Detail: key + " mutates persisted state without the coordinator (ADR 0009 invariant #5)",
				})
			}
		}
	}
	return findings, nil
}

// CheckUncoordinatedMutations is UncoordinatedMutations wired to a test.
func CheckUncoordinatedMutations(t *testing.T, dir string, receivers []string, allowed map[string]string) {
	t.Helper()
	findings, err := UncoordinatedMutations(dir, receivers, allowed)
	if err != nil {
		t.Fatalf("coordinator gate could not run: %v", err)
	}
	for _, finding := range findings {
		t.Errorf("%s", finding.Detail)
	}
}
