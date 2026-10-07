package activation

import (
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

func (i *CatalogIndex) Set(providerID string, entry CatalogEntry) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, existed := i.items[providerID]
	i.items[providerID] = entry
	return existed
}

func (i *CatalogIndex) Delete(providerID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.items[providerID]; !exists {
		return false
	}
	delete(i.items, providerID)
	return true
}
