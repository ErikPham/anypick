package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestMutatingMethodsTakeTheCoordinatorLock enforces ADR 0009 (CONC-01) and
// AGENTS.md invariant #5: every persisted mutation runs under the coordinator.
//
// WithMutationLocks is opt-in at the call site, so without this test a new
// mutating method could be added that simply never takes the lock. It would
// pass review, pass the suite, and then corrupt a real credential file months
// later as a race nobody can reproduce. This turns that mistake into a build
// failure.
//
// The check is deliberately syntactic rather than behavioural: it answers "did
// this method route through the coordinator?", which is the property ADR 0009
// actually promises. The re-entrancy semantics are covered by internal/lock's
// own tests.
func TestMutatingMethodsTakeTheCoordinatorLock(t *testing.T) {
	// parser.ParseDir is deprecated (it ignores build tags when associating
	// files with packages) and this guard only needs the files, not the
	// grouping. This package ships no build-tag variants, so reading the
	// directory and parsing each file is both correct and future-proof.
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the store package: %v", err)
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
			if reason, allowed := unguardedMutations[name]; allowed {
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s is allowlisted with no reason; say why it is safe", name)
				}
				continue
			}
			if !takesCoordinatorLock(fn.Body) {
				missing = append(missing, name+" ("+fset.Position(fn.Pos()).String()+")")
			}
		}
	}

	if len(missing) > 0 {
		t.Errorf("mutating methods bypass the coordinator (ADR 0009 invariant #5): %s\n"+
			"Wrap the body in s.mutate(...) / lock.WithMutationLocks(...), or add it to\n"+
			"unguardedMutations with a written reason.",
			strings.Join(missing, "; "))
	}
}

// mutatesByVerbs reports whether a method name reads as a mutation. The list
// is intentionally broad: a false positive costs one allowlist entry with a
// sentence of justification, while a false negative costs a data race.
func mutatesByVerbs(name string) bool {
	for _, verb := range []string{
		"Write", "Set", "Clear", "Delete", "Patch", "Update", "Create", "Remove",
		"Save", "Upsert", "Insert", "Discard", "Prepare", "Ingest", "Reset",
		"Rotate", "Revoke", "Mark", "Move", "Replace", "Apply",
	} {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

// unguardedMutations is the escape hatch, and every entry must justify itself.
// Adding to this map is a decision, not a workaround — that is the point.
var unguardedMutations = map[string]string{
	// Creates a brand-new temp dir under the staging root and records it in
	// s.prepared behind s.mu. It mutates no existing row and no existing
	// snapshot, so there is nothing for another actor to race against. The
	// commit that matters is WriteMeta, which is guarded.
	"PrepareSnapshot": "creates a fresh staging dir only; no existing state is mutated (commit point is WriteMeta, which is guarded)",
	// Deletes a staging dir this process created, inside the staging root, and
	// is refused outside it. Same reasoning as PrepareSnapshot.
	"DiscardPreparedSnapshot": "removes only a staging dir this process created; no row or snapshot is touched",
}

// takesCoordinatorLock reports whether a function body routes through the
// coordinator. s.mutate is the store's own wrapper; the WithMutationLock
// family is used directly when a mutation spans several provider scopes.
func takesCoordinatorLock(body *ast.BlockStmt) bool {
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
