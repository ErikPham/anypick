package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFacadeBudgetDoesNotGrow is a structural gate, not a ratchet.
//
// `App` exposes every store, so the CLI and the desktop protocol could reach
// persistence directly instead of going through a service. AGENTS.md's
// "CLI/TUI call services, never stores" invariant is therefore enforced here
// by parsing every non-test file in cmd/anypick + internal/desktop and
// reconstructing the dotted selector chain (e.g. `s.Deps.Proxy.Pools`) from
// the AST, then failing the build if any forbidden chain appears.
//
// The walk is structural, not textual: it parses each file and rebuilds the
// selector chain from the parse tree rather than grepping source text, so a
// rename that keeps the coupling is still caught — `a.BindingSvc` no longer
// contains "a.Bindings", but `b.srv.Deps.Store` would still contain
// "Deps.Store", and a re-added raw field matching an old name fails
// immediately. Substring matches inside comments or string literals cannot
// hide a leak.
//
// Every raw store handle that has been routed behind a service boundary is
// forbidden: ANY occurrence fails the build, so the count cannot creep back
// up. This is the locked-in end state of the facade ratchet — the irreducible
// wiring (a.Store, a.Hub, Deps.Store) was the last survivor of the tourniquet
// and is now forbidden too (Decision 7 in SYSTEM_ARCHITECTURE_2026-09-07.md §6).
// Bringing it to zero is the whole point: the budget mechanism is gone because
// there is nothing irreducible left to tolerate.

// facadeForbidden are raw store handles that must never be reached from an
// outer layer again. Each tail is matched against the reconstructed selector
// chain, so the routing that removed them is locked in: re-adding a field that
// matches one of these names, or reaching the raw store through a renamed
// handle, fails the build.
var facadeForbidden = []string{
	"a.Bindings",         // -> a.BindingSvc (bindings.Service)
	"a.Presets",          // -> a.PresetSvc (presets.Service)
	"a.Pools",            // -> a.PoolSvc (pools.Service)
	"a.Runtime.Profiles", // -> a.Runtime.ProfileSvc (profile.Service)
	"Deps.Proxy.Pools",   // -> s.Deps.Proxy.HasPools() / GetPoolContext()
	"a.Leases",           // -> a.Proxy.ReapStaleContext()
	"Deps.Proxy.Jobs",    // -> s.Deps.Proxy.ListPoolLeasesContext()
	"a.Store",            // -> a.Accounts (accounts.Service); the last irreducible wiring
	"Deps.Store",         // -> s.Deps.Accounts (accounts.Service)
	"a.Hub",              // -> a.ProxyHub (hub.Service); the last irreducible wiring
}

// facadeLayers are the packages that must talk to services, not stores.
var facadeLayers = []string{"cmd/anypick", "internal/desktop"}

func TestFacadeBudgetDoesNotGrow(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}

	forbiddenHits := map[string][]string{}

	for _, layer := range facadeLayers {
		dir := filepath.Join(root, filepath.FromSlash(layer))
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				chain := selectorChain(sel)
				if chain == "" {
					return true
				}
				for _, pat := range facadeForbidden {
					if strings.Contains(chain, pat) {
						forbiddenHits[pat] = append(forbiddenHits[pat],
							filepath.ToSlash(rel)+" ("+chain+")")
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", layer, err)
		}
	}

	var problems []string
	for _, pat := range facadeForbidden {
		if hits := forbiddenHits[pat]; len(hits) > 0 {
			problems = append(problems, pat+" is forbidden (routed behind a service): "+
				strings.Join(hits, ", "))
		}
	}

	if len(problems) > 0 {
		t.Errorf("the App facade leaked (AGENTS.md invariant: outer layers call services, not stores):\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// selectorChain reconstructs the dotted chain of a selector expression from the
// parse tree: `a.B.C` becomes "a.B.C". A chain whose root is not a bare
// identifier (e.g. `foo().Bar`) returns "" so it cannot silently match a
// facade pattern.
func selectorChain(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return selectorChain(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return selectorChain(x.X)
	case *ast.ParenExpr:
		return selectorChain(x.X)
	default:
		return ""
	}
}

// itoa renders a non-negative integer without strconv so the debt-gate test
// (same package) can format line numbers. Kept here because it is shared by
// multiple *_test.go files in package app.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
