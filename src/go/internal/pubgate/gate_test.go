package pubgate

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The property this package exists for: a generation commit holds Lock across
// identity + every swap + rollback, so no observer can see a mixed generation.
// Every timing assertion here fails with a message rather than hanging — a
// deadlock in this package would otherwise eat the whole 900s package timeout.
const wait = 2 * time.Second

// A nil Gate must be a usable no-op: registries are constructed without a
// supervisor in unit tests throughout the tree, and every one of them reaches
// Gate.RLock through the registry read path.
func TestNilGateIsANoOp(t *testing.T) {
	var g *Gate
	var held []string

	// Reaching the line after each Unlock is the assertion: a nil Gate that
	// dereferenced itself would panic here instead.
	g.RLock()
	held = append(held, "read")
	g.RUnlock()
	g.Lock()
	held = append(held, "write")
	g.Unlock()

	if len(held) != 2 {
		t.Fatalf("critical sections ran %d times, want 2 (read + write)", len(held))
	}
}

func TestWriteLockExcludesReaders(t *testing.T) {
	g := New()
	var readRan int32
	g.Lock()

	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		g.RLock()
		defer g.RUnlock()
		// The section has to do something observable: the assertion below is
		// that this did not happen until after Unlock.
		atomic.StoreInt32(&readRan, 1)
	}()

	select {
	case <-acquired:
		t.Fatal("RLock proceeded while the write lock was held — an observer can see a mixed generation")
	case <-time.After(100 * time.Millisecond):
	}
	if atomic.LoadInt32(&readRan) != 0 {
		t.Fatal("read critical section ran while the write lock was held")
	}

	g.Unlock()

	select {
	case <-acquired:
	case <-time.After(wait):
		t.Fatal("RLock never acquired after Unlock")
	}
	if atomic.LoadInt32(&readRan) != 1 {
		t.Error("RLock returned but the read section never ran")
	}
}

// A reader blocked behind a commit observes the state as it is after the
// commit finished — including a rollback — never a half-applied one.
func TestBlockedReaderSeesTheSettledState(t *testing.T) {
	g := New()
	state := "A"

	g.Lock()
	blocked := make(chan string, 1)
	go func() {
		g.RLock()
		defer g.RUnlock()
		blocked <- state
	}()

	// Give the goroutine a chance to reach RLock and park; if it had raced
	// through, the case below would already have fired.
	time.Sleep(50 * time.Millisecond)
	select {
	case seen := <-blocked:
		t.Fatalf("reader observed %q mid-commit, want it to park until the commit settled", seen)
	default:
	}

	state = "B" // publish
	state = "A" // rollback: the swap was rejected
	g.Unlock()

	select {
	case seen := <-blocked:
		if seen != "A" {
			t.Errorf("blocked reader saw %q, want the post-rollback value A", seen)
		}
	case <-time.After(wait):
		t.Fatal("blocked reader never ran after Unlock")
	}
}

// Readers must not serialise against each other: registries are read on hot
// paths, and a gate that excluded readers would turn every lookup into a
// global bottleneck.
func TestReadersShareTheLock(t *testing.T) {
	g := New()
	const readers = 8

	var (
		entered int32
		wg      sync.WaitGroup
	)
	wg.Add(readers)
	start := make(chan struct{})
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			<-start
			g.RLock()
			// Counting under the read lock: the gate is only doing its job if
			// every reader is inside at once, not one at a time.
			atomic.AddInt32(&entered, 1)
			g.RUnlock()
		}()
	}
	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wait):
		t.Fatalf("%d concurrent RLocks did not all proceed; readers are serialising", readers)
	}
	if got := atomic.LoadInt32(&entered); got != readers {
		t.Errorf("%d readers entered the critical section, want %d", got, readers)
	}
}

// Two gates from New() are independent. Sharing one by accident would make
// unrelated subsystems block on each other.
func TestGatesAreIndependent(t *testing.T) {
	held, free := New(), New()
	held.Lock()
	defer held.Unlock()

	var reached bool
	acquired := make(chan struct{})
	go func() {
		free.Lock()
		reached = true
		free.Unlock()
		close(acquired)
	}()

	select {
	case <-acquired:
	case <-time.After(wait):
		t.Fatal("Lock on one gate blocked a different gate")
	}
	if !reached {
		t.Error("the second gate's critical section never ran")
	}
}
