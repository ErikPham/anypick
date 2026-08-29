// Package pubgate is the shared publication lock for plugin-host generation
// commits (GO-PARITY-M7).
//
// Provider/client registries and catalog indexes each have their own mutex, so
// holding the supervisor lock does not make Get/Lookup/IDs atomic with identity
// publish. Every registry reader takes Gate.RLock; a replacement commit holds
// Gate.Lock across identity + every swap + rollback so observers never see a
// mixed generation, and never see a B value that is later rolled back.
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
