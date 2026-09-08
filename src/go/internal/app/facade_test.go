package app

import (
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
// Each pattern below is a raw store handle reachable from an outer layer. The
// number is how many times it is used today. Adding one fails the build;
// removing one does not, and when a count reaches zero the entry should be
// deleted so the budget cannot creep back up.
// The Proxy Hub's two account reads now go through accounts.Service. The
// remaining store access budget is legacy transfer/activation wiring; it is a
// ratchet and must shrink, never grow.
//
// TODO(gate:facade-budget): this map is a tourniquet, not a cure — 82 call
// sites still hold a raw persistence handle, so the layering rule is upheld by
// counting rather than by types. `a.Store` dropped 20 -> 3 by routing
// ListAccounts/Get/GetActive through accounts.Service and
// GetProxyConfig/ReadProxyState through proxy.Service, which already owns the
// store. Then `Deps.Store` went 30 -> 8 by routing ListAccounts/GetActive/Get
// to accounts.Service, GetProxyConfig/ReadProxyState to proxy.Service and
// PatchMetaContext to accounts.Service.PatchMeta.
//
// The 11 that remain are irreducible, and each kind is worth naming so nobody
// spends an afternoon on it again: `transfer.ExportEnvelope`,
// `transfer.ExportSelectedBackup`, `inspections.Inspect`/`Restore` all take the
// store because the transfer and inspection subsystems are the store's peers;
// one `Store:` struct wiring; one nil guard; one `s.Deps.Store.DB` handed to
// modelcache, which needs the *sql.DB rather than an account.
//
// Target: <= 40 by 2026-10-08, then replace the whole map with an AST gate
// forbidding `internal/store` imports from cmd/anypick and internal/desktop.
// Decision 7 in SYSTEM_ARCHITECTURE_2026-09-07.md §6.
var facadeBudget = map[string]int{
	"a.Store": 3,
	// Receiver-independent on purpose. This used to be "s.Deps.Store", and
	// splitting buildSnapshot into methods on a `b *snapshotBuilder` silently
	// dropped the count from 30 to 25 — not because any coupling went away,
	// but because the call sites now spell it `b.srv.Deps.Store`. A ratchet
	// that measures how a variable is named is not measuring the thing it
	// claims to. Match on the coupling, never on the receiver.
	"Deps.Store":         8,
	"a.Hub":              2,
	// a.Bindings dropped 11 -> 0: the App now exposes bindings.Service
	// (App.BindingSvc), so the CLI/desktop/TUI reach bindings through a service
	// boundary instead of the raw store. The field is named BindingSvc on
	// purpose — a.BindingsSvc would still match the old "a.Bindings" prefix.
	// a.Presets dropped 3 -> 0 the same way: App.PresetSvc is a presets.Service,
	// and a.PresetSvc does not contain the literal "a.Presets" (the
	// "a.PresetsSvc" prefix would have kept counting it).
	// a.Pools dropped 7 -> 0: App.PoolSvc is a pools.Service, and a.PoolSvc does
	// not contain the literal "a.Pools" (the "a.PoolsSvc" prefix would have
	// kept counting it).
	"a.Runtime.Profiles": 9,
	"Deps.Proxy.Pools":   2,
	"a.Leases":           1,
	"Deps.Proxy.Jobs":    1,
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
	for _, layer := range facadeLayers {
		dir := filepath.Join(root, filepath.FromSlash(layer))
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			text := string(body)
			for pattern := range facadeBudget {
				n := strings.Count(text, pattern)
				if n == 0 {
					continue
				}
				counts[pattern] += n
				rel, _ := filepath.Rel(root, path)
				locations[pattern] = append(locations[pattern],
					filepath.ToSlash(rel)+" ("+itoa(n)+")")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", layer, err)
		}
	}

	var problems []string
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
