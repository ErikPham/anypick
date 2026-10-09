// Package pubgate is the shared publication lock for provider/client registry
// generation commits.
//
// Provider/client registries and catalog indexes each have their own mutex, so
// holding the supervisor lock does not make Get/Lookup/IDs atomic with identity
// publish. Every registry reader takes Gate.RLock; a replacement commit was to
// hold Gate.Lock across identity + every swap + rollback so observers never see
// a mixed generation, and never see a B value that is later rolled back.
//
// THE WRITER HALF HAS NO CALLER TODAY. ADR 0021 removed the executable plugin
// host and ADR-0032 deleted the seam that would have used this lock, so nothing
// in the tree takes Gate.Lock outside this package's own test. The read half is
// live (ten RLock sites across the four registries), so the gate still
// serializes nothing while costing one uncontended RWMutex read per lookup.
//
// It is kept, not deleted, on its own reasoning rather than any record's: a
// runtime that can swap a registry identity in place would need exactly this
// lock, and rediscovering that requirement costs more than carrying an
// uncontended read. ADR-0032 does not mention this package. If in-tree provider
// registration ever grows a live writer, decide then whether this earns its
// place — and if that never happens, delete this package and the ten RLock
// sites together.
package pubgate

import "sync"

// Gate is a nil-safe RWMutex. A nil Gate is a no-op so unit tests that
// construct registries without a supervisor still work.
type Gate struct {
	mu sync.RWMutex
}

// New returns an independent publication gate.
func New() *Gate { return &Gate{} }

// RLock is taken by Get, Has, IDs, and Lookup.
func (g *Gate) RLock() {
	if g != nil {
		g.mu.RLock()
	}
}

// RUnlock pairs with RLock.
func (g *Gate) RUnlock() {
	if g != nil {
		g.mu.RUnlock()
	}
}

// Lock is taken by the generation-commit writer for the entire replacement
// transaction, including rollback.
func (g *Gate) Lock() {
	if g != nil {
		g.mu.Lock()
	}
}

// Unlock pairs with Lock.
func (g *Gate) Unlock() {
	if g != nil {
		g.mu.Unlock()
	}
}
