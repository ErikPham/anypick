package upstream

// Ring-selection contracts: what a rotating pool reads (Eligible, Cooling) and
// what a sticky one reads (Ordered). The difference between them is the whole
// rotation feature, so it is asserted directly rather than only through a
// started proxy.

import (
	"strings"
	"testing"
)

// TestEligiblePreservesRingOrder is what makes rotation possible: Ordered
// promotes the last active account to the head, so a rotating ring built on it
// would keep serving the same account.
func TestEligiblePreservesRingOrder(t *testing.T) {
	guard := NewGuard(GuardOptions{Enabled: true, CooldownMs: 60_000})
	candidates := []Candidate{testCandidate{"a"}, testCandidate{"b"}, testCandidate{"c"}}

	if err := guard.Activate("c"); err != nil {
		t.Fatal(err)
	}
	if err := guard.Exhausted("b", "c", nil); err != nil {
		t.Fatal(err)
	}

	ordered, err := guard.Ordered(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].GetAccountName() != "c" {
		t.Fatalf("Ordered head = %q, want the active account 'c'", ordered[0].GetAccountName())
	}

	eligible, err := guard.Eligible(candidates)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, c := range eligible {
		got = append(got, c.GetAccountName())
	}
	if strings.Join(got, ",") != "a,c" {
		t.Fatalf("Eligible = %v, want [a c] in ring order with the cooling member dropped", got)
	}
}

// TestCoolingOrdersByRecovery keeps the last resort honest: when every member
// is cooling, the ring attempts the one that will recover soonest.
func TestCoolingOrdersByRecovery(t *testing.T) {
	guard := NewGuard(GuardOptions{Enabled: true, CooldownMs: 60_000, ServeWhenAllCooling: true})
	candidates := []Candidate{testCandidate{"a"}, testCandidate{"b"}, testCandidate{"c"}}

	// Exhaust in an order that is not the ring order, and with different
	// retry-after hints so the deadlines differ.
	short := int64(2_000)
	long := int64(600_000)
	if err := guard.Exhausted("a", "b", &long); err != nil {
		t.Fatal(err)
	}
	if err := guard.Exhausted("c", "", &long); err != nil {
		t.Fatal(err)
	}
	if err := guard.Exhausted("b", "", &short); err != nil {
		t.Fatal(err)
	}

	cooling, err := guard.Cooling(candidates)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, c := range cooling {
		got = append(got, c.GetAccountName())
	}
	if strings.Join(got, ",") != "b,a,c" {
		t.Fatalf("Cooling = %v, want [b a c] (soonest first)", got)
	}
	if !guard.ServesWhenAllCooling() {
		t.Fatal("the policy must be reported as configured")
	}
}

type testCandidate struct{ account string }

func (c testCandidate) GetAccountName() string { return c.account }
