package upstream

import (
	"path/filepath"
	"sync"
	"testing"
)

type guardTestCandidate struct{ account string }

func (c guardTestCandidate) GetAccountName() string { return c.account }

func newTestGuard(t *testing.T) *Guard {
	t.Helper()
	return NewGuard(GuardOptions{
		Enabled:      true,
		CooldownMs:   60_000,
		AccountNames: []string{"a", "b", "c"},
		StatePath:    filepath.Join(t.TempDir(), "state.json"),
		ProviderID:   "test",
	})
}

// TestGuardConcurrentUseIsRaceFree is the regression test for the crash this
// package shipped with. load() returned the live Cooling map by value,
// Ordered() read it after releasing the lock, and mutate() wrote to it under a
// second mutex. Two concurrent pool requests therefore produced
// `fatal error: concurrent map iteration and map write` — an unrecoverable
// process kill, not a recoverable panic.
//
// It only reproduces reliably under -race or under load, so it must stay in
// the -race run. Deleting this test re-opens a crash, not a style nit.
func TestGuardConcurrentUseIsRaceFree(t *testing.T) {
	g := newTestGuard(t)
	candidates := []Candidate{
		guardTestCandidate{"a"},
		guardTestCandidate{"b"},
		guardTestCandidate{"c"},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := g.Ordered(candidates); err != nil {
					t.Error(err)
					return
				}
				if err := g.Exhausted("a", "b", nil); err != nil {
					t.Error(err)
					return
				}
				if err := g.Activate("b"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestGuardExhaustedCollapsesConcurrentReports pins the anti-stampede rule:
// thirty-two requests that all see member "a" rate-limited must produce one
// cooldown and one audit event, not thirty-two serialized file writes.
func TestGuardExhaustedCollapsesConcurrentReports(t *testing.T) {
	g := newTestGuard(t)
	const burst = 32

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := g.Exhausted("a", "b", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()

	state, err := ReadState(g.options.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 1 {
		t.Fatalf("burst of %d reports produced %d audit events, want 1 — the stampede is not collapsed",
			burst, len(state.Events))
	}
	if _, ok := state.Cooling["a"]; !ok {
		t.Fatalf("member a is not cooling after Exhausted: %+v", state.Cooling)
	}
	if state.ActiveAccount != "b" {
		t.Fatalf("ActiveAccount = %q, want %q", state.ActiveAccount, "b")
	}
}

// TestGuardLoadDoesNotShareLiveState pins the ownership rule directly: the
// State a caller receives must be a copy, so mutating it cannot corrupt the
// Guard or race a concurrent reader.
func TestGuardLoadDoesNotShareLiveState(t *testing.T) {
	g := newTestGuard(t)
	if err := g.Exhausted("a", "b", nil); err != nil {
		t.Fatal(err)
	}

	g.mu.Lock()
	first, err := g.loadLocked()
	g.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	delete(first.Cooling, "a")
	first.Events = nil

	g.mu.Lock()
	second, err := g.loadLocked()
	g.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := second.Cooling["a"]; !ok {
		t.Fatal("caller's copy aliases the Guard's live Cooling map — deleting through it mutated the guard")
	}
	if len(second.Events) == 0 {
		t.Fatal("caller's copy aliases the Guard's live Events slice")
	}
}
