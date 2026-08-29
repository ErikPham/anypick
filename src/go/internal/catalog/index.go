package catalog

import (
	"fmt"
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

// SetIfAbsent inserts the entry only when its id is free, reporting whether
// it was stored. This preserves the richer built-in catalog shape against a
// compatible-provider duplicate (OpenRouter is the canonical case).
func (i *Index) SetIfAbsent(entry Provider) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.items[entry.ID]; exists {
		return false
	}
	i.items[entry.ID] = entry
	return true
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

// RecoverSwap CAS-replaces the previously committed catalog value, or inserts
// when previous is nil. Callers already hold the publication write lock.
func (i *Index) RecoverSwap(previous *Provider, next Provider) error {
	if next.ID == "" {
		return fmt.Errorf("recovery swap requires a catalog id")
	}
	if previous != nil && previous.ID != next.ID {
		return fmt.Errorf("recovery id mismatch: %s vs %s", previous.ID, next.ID)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	current, exists := i.items[next.ID]
	switch {
	case exists && previous != nil && equalProvider(current, *previous):
	case !exists && previous == nil:
	default:
		return fmt.Errorf("catalog %q changed since recovery started", next.ID)
	}
	i.items[next.ID] = next
	return nil
}

// RecoverRemove CAS-deletes previous when it is still the stored value.
func (i *Index) RecoverRemove(previous Provider) error {
	return i.RecoverUndo(previous, nil)
}

// RecoverUndo reverses a RecoverSwap of current. restore=nil deletes the id.
func (i *Index) RecoverUndo(current Provider, restore *Provider) error {
	if current.ID == "" {
		return fmt.Errorf("recovery undo requires a catalog id")
	}
	if restore != nil && restore.ID != current.ID {
		return fmt.Errorf("recovery id mismatch: %s vs %s", restore.ID, current.ID)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	existing, exists := i.items[current.ID]
	if !exists || !equalProvider(existing, current) {
		return fmt.Errorf("catalog %q changed since recovery started", current.ID)
	}
	if restore == nil {
		delete(i.items, current.ID)
		return nil
	}
	i.items[current.ID] = *restore
	return nil
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
