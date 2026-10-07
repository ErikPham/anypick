package catalog

import (
	"slices"
	"sort"
	"sync"

	"github.com/outof0/anypick/go/internal/pubgate"
)

// Index is the runtime catalog view. Plugin activation writes it during
// startup, and plugin-host recovery (GO-PARITY-M7) rewrites it while other
// goroutines may be resolving endpoints — so every access is synchronized.
// Entries are only added or replaced by id; readers never observe a torn
// value. Get-style readers also take the shared publication gate so they
// cannot observe a mixed generation during a replacement commit.
type Index struct {
	gate  *pubgate.Gate
	mu    sync.RWMutex
	items map[string]Provider
}

// NewIndex wraps a seed map. The map is copied; later caller-side mutations
// are invisible to the index.
func NewIndex(entries map[string]Provider) *Index {
	items := make(map[string]Provider, len(entries))
	for id, entry := range entries {
		items[id] = entry
	}
	return &Index{items: items}
}

// SetGate installs the shared publication lock used by Lookup and IDs.
func (i *Index) SetGate(gate *pubgate.Gate) {
	if i != nil {
		i.gate = gate
	}
}

// Lookup returns one entry; found=false when absent.
func (i *Index) Lookup(id string) (Provider, bool) {
	if i == nil {
		return Provider{}, false
	}
	i.gate.RLock()
	defer i.gate.RUnlock()
	i.mu.RLock()
	defer i.mu.RUnlock()
	entry, ok := i.items[id]
	return entry, ok
}

// IDs lists catalog ids in sorted order under the publication gate.
func (i *Index) IDs() []string {
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
func (i *Index) Set(entry Provider) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, existed := i.items[entry.ID]
	i.items[entry.ID] = entry
	return existed
}

// Delete removes one id if present, reporting whether it was.
func (i *Index) Delete(id string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.items[id]; !exists {
		return false
	}
	delete(i.items, id)
	return true
}

func equalProvider(a, b Provider) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Description == b.Description &&
		a.APIStyle == b.APIStyle &&
		a.DefaultEndpoint == b.DefaultEndpoint &&
		a.AnthropicEndpoint == b.AnthropicEndpoint &&
		slices.Equal(a.Protocols, b.Protocols) &&
		slices.Equal(a.FirstPartyClientIDs, b.FirstPartyClientIDs)
}
