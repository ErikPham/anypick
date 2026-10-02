package pools

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
func TestMutatingMethodsTakeTheCoordinatorLock(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the pools package: %v", err)
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
			// Only the table-owning Store self-locks. Service methods forward
			// into Store and would false-positive every time.
			if !recvIsStore(fn) {
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

func recvIsStore(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		ident, ok := t.X.(*ast.Ident)
		return ok && ident.Name == "Store"
	case *ast.Ident:
		return t.Name == "Store"
	}
	return false
}

func mutatesByVerbs(name string) bool {
	for _, verb := range []string{
		"Write", "Set", "Clear", "Delete", "Patch", "Update", "Create", "Remove",
		"Save", "Upsert", "Insert", "Discard", "Prepare", "Ingest", "Reset",
		"Rotate", "Revoke", "Mark", "Move", "Replace", "Apply", "Disable",
	} {
		if strings.HasPrefix(name, verb) {
			return true
		}
	}
	return false
}

var unguardedMutations = map[string]string{}

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
