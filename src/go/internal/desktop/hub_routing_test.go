package desktop

import "testing"

func TestHubRoutingCandidatesOfferProvidersAcrossProviders(t *testing.T) {
	sources := map[string]hubRoutingSource{
		"a": {id: "a", hub: "h", providerID: "p1", name: "A", label: "A", enabled: true},
		"b": {id: "b", hub: "h", providerID: "p2", name: "B", label: "B", enabled: true},
	}
	cands := hubRoutingCandidates([]string{"a", "b"}, sources, "overlap-1")
	if len(cands) != 4 {
		t.Fatalf("candidates = %+v, want 2 provider + 2 account", cands)
	}
	if cands[0].id != "provider/p1" || cands[1].id != "provider/p2" {
		t.Fatalf("provider choices must lead: %+v", cands)
	}
	if cands[0].actionID != "overlap-1:provider/p1" || !cands[0].providerChoice {
		t.Fatalf("provider candidate = %+v", cands[0])
	}
	if cands[0].detail != "1 account" || cands[1].detail != "1 account" {
		t.Fatalf("provider detail = %q / %q", cands[0].detail, cands[1].detail)
	}
	if cands[2].id != "a" || cands[2].providerChoice || cands[3].id != "b" {
		t.Fatalf("account candidates = %+v", cands[2:])
	}

	solo := map[string]hubRoutingSource{
		"a": {id: "a", hub: "h", providerID: "p1", name: "A", enabled: true},
		"c": {id: "c", hub: "h", providerID: "p1", name: "C", enabled: true},
	}
	soloCands := hubRoutingCandidates([]string{"a", "c"}, solo, "choice-1")
	if len(soloCands) != 2 {
		t.Fatalf("single-provider group must stay account-only: %+v", soloCands)
	}
}
