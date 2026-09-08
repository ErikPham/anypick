package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestFacadeBudgetDoesNotGrow is a ratchet, not a fix.
//
// `App` exposes every store, so the CLI and the desktop protocol can reach
// persistence directly instead of going through a service. That is why
// AGENTS.md's "CLI/TUI call services, never stores" has never been enforceable:
// the type system offers no objection. Routing ~100 call sites through service
// methods is a refactor of its own, and until it is done the honest thing is to
// make sure it does not get worse.
//
// Two mechanisms enforce this, both walking cmd/anypick + internal/desktop
// (non-test) and reconstructing every selector chain from the parse tree rather
// than grepping source text:
//
//   - facadeForbidden: raw store handles that have been fully routed behind a
//     service boundary. ANY occurrence fails the build, so the count cannot
//     creep back up. Structural detection means a rename that keeps the
//     coupling is still caught — `a.BindingSvc` no longer contains "a.Bindings",
//     but `b.srv.Deps.Store` would still contain "Deps.Store", and a re-added
//     raw field matching an old name fails immediately.
//   - facadeBudget: irreducible store couplings that remain — the composition
//     root wiring at cmd/anypick/main.go, where the App hands its raw store and
//     hub store to inner Deps structs that legitimately own them. Their budget
//     is 1 each and must never grow; a second occurrence is a new leak.
//
// The walk is structural, not textual: it parses each file and rebuilds the
// dotted selector chain (e.g. `s.Deps.Proxy.Pools`) from the AST, so the old
// `b.srv.Deps.Store` undercount trick and substring matches inside comments or
// string literals can no longer hide a leak.
//
// TODO(gate:facade-budget): the irreducible wiring (a.Store, a.Hub) is the last
// survivor of the tourniquet. Once activation/runtime stop requiring the raw
// store at the boundary it can be forbidden too. Decision 7 in
// SYSTEM_ARCHITECTURE_2026-09-07.md §6.

// facadeForbidden are raw store handles that have been fully routed behind a
// service boundary and must never be reached from an outer layer again. Each
// tail is matched against the reconstructed selector chain, so the routing
// that removed them is locked in: re-adding a field that matches one of these
// names, or reaching the raw store through a renamed handle, fails the build.
var facadeForbidden = []string{
	"a.Bindings",         // -> a.BindingSvc (bindings.Service)
	"a.Presets",          // -> a.PresetSvc (presets.Service)
	"a.Pools",            // -> a.PoolSvc (pools.Service)
	"a.Runtime.Profiles", // -> a.Runtime.ProfileSvc (profile.Service)
	"Deps.Proxy.Pools",   // -> s.Deps.Proxy.HasPools() / GetPoolContext()
	"a.Leases",           // -> a.Proxy.ReapStaleContext()
	"Deps.Proxy.Jobs",    // -> s.Deps.Proxy.ListPoolLeasesContext()
}

// facadeBudget is the irreducible store coupling that remains: transfer,
// inspection and modelcache subsystems are the store's peers (not its clients),
// and the composition root hands the raw store/hub store to inner Deps structs
// that own them (activation.Deps takes the store itself). These budgets must
// never grow; when one of them can be routed behind a service it should move to
// facadeForbidden instead. The structural walk makes the count robust to the
// old `b.srv.Deps.Store` rename trick and to substring matches in comments.
var facadeBudget = map[string]int{
	// cmd/anypick: transfer.ExportEnvelope + a desktop.Deps wiring (accounts.go,
	// main.go). Irreducible until transfer takes a service, not the store.
	"a.Store": 3,
	// internal/desktop: transfer.ExportEnvelope/ExportSelectedBackup,
	// inspections.Inspect/Restore, a nil guard and the modelcache *sql.DB handoff
	// (handlers.go, server.go). Irreducible: those subsystems are the store's peers.
	"Deps.Store": 8,
	// cmd/anypick: desktop.Deps wiring at main.go and misc.go — activation.Deps
	// owns the hub store, so the boundary cannot avoid the raw handle yet.
	"a.Hub": 2,
}

// facadeLayers are the packages that must talk to services, not stores.
var facadeLayers = []string{"cmd/anypick", "internal/desktop"}

func TestFacadeBudgetDoesNotGrow(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}

	counts := map[string]int{}
	locations := map[string][]string{}
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
				for pat := range facadeBudget {
					if leafMatchesBudget(sel, chain, pat) {
						counts[pat]++
						locations[pat] = append(locations[pat],
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

	patterns := make([]string, 0, len(facadeBudget))
	for p := range facadeBudget {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)

	for _, p := range patterns {
		budget := facadeBudget[p]
		actual := counts[p]
		switch {
		case actual > budget:
			problems = append(problems, p+" grew from "+itoa(budget)+" to "+itoa(actual)+
				" — route the new call through a service")
		case actual == 0:
			problems = append(problems, p+" is no longer used; delete it from facadeBudget "+
				"so the count cannot creep back up")
		case actual < budget:
			t.Logf("%s shrank from %d to %d (%s) — lower the budget to lock it in",
				p, budget, actual, strings.Join(locations[p], ", "))
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

// leafMatchesBudget reports whether sel is the leaf store access described by
// pat (PREFIX.FIELD): its selector name is FIELD and the chain of the value it
// is selected from ends with PREFIX. Matching on the leaf selector — rather
// than testing the whole chain for the substring — avoids double-counting
// nested accesses: `s.Deps.Store.DB` would otherwise tally both `s.Deps.Store`
// and `s.Deps.Store.DB`.
func leafMatchesBudget(sel *ast.SelectorExpr, _ string, pat string) bool {
	dot := strings.LastIndex(pat, ".")
	if dot < 0 {
		return false
	}
	field := pat[dot+1:]
	prefix := pat[:dot]
	if sel.Sel.Name != field {
		return false
	}
	xChain := selectorChain(sel.X)
	return xChain == prefix || strings.HasSuffix(xChain, "."+prefix)
}

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
