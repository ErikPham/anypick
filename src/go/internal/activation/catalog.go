package activation

import (
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/outof0/anypick/go/internal/pubgate"
)

// CatalogIndex is the plan-time provider classification input
// (deps.Catalog). Plugin activation writes it during startup, and plugin-host
// recovery (GO-PARITY-M7) rewrites it while plans may be materializing in
// other goroutines — so every access is synchronized and no reader can observe
// a torn map. Lookup also takes the shared publication gate so a replacement
// commit is atomic with provider/client registry reads.
type CatalogIndex struct {
	gate  *pubgate.Gate
	mu    sync.RWMutex
	items map[string]CatalogEntry
}

// NewCatalogIndex wraps a seed map. The map is copied; later caller-side
// mutations are invisible to the index.
func NewCatalogIndex(entries map[string]CatalogEntry) *CatalogIndex {
	items := make(map[string]CatalogEntry, len(entries))
	for id, entry := range entries {
		items[id] = entry
	}
	return &CatalogIndex{items: items}
}

// SetGate installs the shared publication lock used by Lookup and IDs.
func (i *CatalogIndex) SetGate(gate *pubgate.Gate) {
	if i != nil {
		i.gate = gate
	}
}

// Lookup returns one entry; found=false when absent.
func (i *CatalogIndex) Lookup(providerID string) (CatalogEntry, bool) {
	if i == nil {
		return CatalogEntry{}, false
	}
	i.gate.RLock()
	defer i.gate.RUnlock()
	i.mu.RLock()
	defer i.mu.RUnlock()
	entry, ok := i.items[providerID]
	return entry, ok
}

// IDs lists classification ids in sorted order under the publication gate.
func (i *CatalogIndex) IDs() []string {
	if i == nil {
		return nil
	}
	i.gate.RLock()
	defer i.gate.RUnlock()
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]string, 0, len(i.items))
	for id := range i.items {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Set inserts or replaces one entry, reporting whether it existed.
func (i *CatalogIndex) Set(providerID string, entry CatalogEntry) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, existed := i.items[providerID]
	i.items[providerID] = entry
	return existed
}

// SetIfAbsent inserts the entry only when its id is free, reporting whether it
// was stored (compatible providers intentionally preserve a richer built-in
// classification).
func (i *CatalogIndex) SetIfAbsent(providerID string, entry CatalogEntry) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.items[providerID]; exists {
		return false
	}
	i.items[providerID] = entry
	return true
}

// Delete removes one id if present, reporting whether it was.
func (i *CatalogIndex) Delete(providerID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.items[providerID]; !exists {
		return false
	}
	delete(i.items, providerID)
	return true
}

// RecoverSwap CAS-replaces the previously committed classification, or inserts
// when previous is nil. Callers already hold the publication write lock.
func (i *CatalogIndex) RecoverSwap(id string, previous *CatalogEntry, next CatalogEntry) error {
	if id == "" {
		return fmt.Errorf("recovery swap requires a catalog id")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	current, exists := i.items[id]
	switch {
	case exists && previous != nil && equalCatalogEntry(current, *previous):
	case !exists && previous == nil:
	default:
		return fmt.Errorf("catalog %q changed since recovery started", id)
	}
	i.items[id] = next
	return nil
}

// RecoverRemove CAS-deletes previous when it is still the stored value.
func (i *CatalogIndex) RecoverRemove(id string, previous CatalogEntry) error {
	return i.RecoverUndo(id, previous, nil)
}

// RecoverUndo reverses a RecoverSwap of current. restore=nil deletes the id.
func (i *CatalogIndex) RecoverUndo(id string, current CatalogEntry, restore *CatalogEntry) error {
	if id == "" {
		return fmt.Errorf("recovery undo requires a catalog id")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	existing, exists := i.items[id]
	if !exists || !equalCatalogEntry(existing, current) {
		return fmt.Errorf("catalog %q changed since recovery started", id)
	}
	if restore == nil {
		delete(i.items, id)
		return nil
	}
	i.items[id] = *restore
	return nil
}

func equalCatalogEntry(a, b CatalogEntry) bool {
	return a.Name == b.Name && a.APIStyle == b.APIStyle && slices.Equal(a.Protocols, b.Protocols)
}
