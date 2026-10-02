package hub

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/config"
)

// hubQuotas remembers which hub sources are cooling down after a quota-class
// failure. It exists for the Proxy Hub only: a pool proxy has its own guard
// (internal/upstream) keyed per provider, while a hub serves one request across
// accounts that may belong to different providers, so its key is the source
// itself — provider and account.
//
// The state is a small JSON file beside the hub's own runtime files, so a hub
// restart does not immediately re-hammer an account the vendor just refused.
type Quotas struct {
	mu       sync.Mutex
	path     string
	now      func() time.Time
	enabled  bool
	cooldown time.Duration
	// serveWhenAllCooling keeps a hub serving when every candidate is cooling:
	// the soonest-to-recover one is tried and the vendor's verdict is passed
	// through, rather than refusing locally with our own guess.
	serveWhenAllCooling bool
	// deadlines is source key → unix milliseconds the cooldown ends.
	deadlines map[string]int64
}

// quotaState is the persisted shape. It carries no credential material: keys are
// source identities, values are deadlines.
type quotaState struct {
	Version   int              `json:"version"`
	Cooling   map[string]int64 `json:"cooling"`
	UpdatedAt string           `json:"updatedAt"`
}

// NewQuotas builds the hub's guard from the validated global policy. A nil
// return means "no guard": every candidate is always eligible and no failure is
// ever attributed to an account.
func NewQuotas(policy config.QuotaGuardPolicy, path string) *Quotas {
	return newHubQuotas(policy, path, time.Now)
}

// newHubQuotas is the seam tests use to control the clock.
func newHubQuotas(policy config.QuotaGuardPolicy, path string, now func() time.Time) *Quotas {
	if !policy.Enabled {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	cooldown := time.Duration(policy.CooldownMinutes) * time.Minute
	if cooldown <= 0 {
		cooldown = time.Hour
	}
	q := &Quotas{
		path:                path,
		now:                 now,
		enabled:             true,
		cooldown:            cooldown,
		serveWhenAllCooling: policy.ServeWhenAllCooling,
		deadlines:           map[string]int64{},
	}
	q.load()
	return q
}

// SourceKey is the guard's identity for one hub source. Provider and account
// together: two providers can both have an account called "work".
func SourceKey(providerID, account string) string {
	return providerID + "\x00" + account
}

// keyOf names the source at index i in cfg.
func keyOf(cfg Config, index int) string {
	if index < 0 || index >= len(cfg.Sources) {
		return ""
	}
	return SourceKey(cfg.Sources[index].ProviderID, cfg.Sources[index].Account)
}

// isCooling reports whether the source is inside a cooldown window.
func (q *Quotas) isCooling(key string) bool {
	if q == nil || key == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.coolingLocked(key)
}

func (q *Quotas) coolingLocked(key string) bool {
	until, ok := q.deadlines[key]
	if !ok {
		return false
	}
	if q.now().UnixMilli() >= until {
		delete(q.deadlines, key)
		return false
	}
	return true
}

// exhaust starts a cooldown for the source. retryAfterMs is the vendor's own
// hint when it sent one; a present value is honoured down to one second, an
// absent one falls back to the configured cooldown.
func (q *Quotas) exhaust(key string, retryAfterMs *int64) {
	if q == nil || !q.enabled || key == "" {
		return
	}
	cooldown := q.cooldown
	if retryAfterMs != nil {
		cooldown = time.Duration(*retryAfterMs) * time.Millisecond
	}
	if cooldown < time.Second {
		cooldown = time.Second
	}
	q.mu.Lock()
	q.deadlines[key] = q.now().Add(cooldown).UnixMilli()
	q.persistLocked()
	q.mu.Unlock()
}

// servesWhenAllCooling is the policy for a fully cooled candidate set.
func (q *Quotas) servesWhenAllCooling() bool { return q != nil && q.serveWhenAllCooling }

// clear drops a cooldown early: the account just served a request, so whatever
// window was recorded no longer describes it.
func (q *Quotas) clear(key string) {
	if q == nil || !q.enabled || key == "" {
		return
	}
	q.mu.Lock()
	_, existed := q.deadlines[key]
	delete(q.deadlines, key)
	if existed {
		q.persistLocked()
	}
	q.mu.Unlock()
}

// partition splits candidates into the ones that may be tried now and the ones
// inside a cooldown window, preserving order.
func (q *Quotas) partition(cfg *Config, candidates []int) (eligible, cooling []int) {
	for _, index := range candidates {
		if q.isCooling(keyOf(*cfg, index)) {
			cooling = append(cooling, index)
			continue
		}
		eligible = append(eligible, index)
	}
	return eligible, cooling
}

// soonestToRecover returns the cooling source that will be eligible first, or
// "" when nothing is cooling.
func (q *Quotas) soonestToRecover(keys []string) string {
	if q == nil {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	best, bestUntil := "", int64(0)
	for _, key := range keys {
		if !q.coolingLocked(key) {
			continue
		}
		until := q.deadlines[key]
		if best == "" || until < bestUntil {
			best, bestUntil = key, until
		}
	}
	return best
}

func (q *Quotas) load() {
	if q.path == "" {
		return
	}
	raw, err := readFileIfPresent(q.path)
	if err != nil || len(raw) == 0 {
		return
	}
	var state quotaState
	if json.Unmarshal(raw, &state) != nil {
		return
	}
	now := q.now().UnixMilli()
	for key, until := range state.Cooling {
		if until > now {
			q.deadlines[key] = until
		}
	}
}

// persistLocked writes the cooling map. Advisory state: a write failure must not
// fail the request that produced it, so it is dropped rather than propagated.
func (q *Quotas) persistLocked() {
	if q.path == "" {
		return
	}
	keys := make([]string, 0, len(q.deadlines))
	for key := range q.deadlines {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state := quotaState{
		Version: 1, Cooling: map[string]int64{},
		UpdatedAt: q.now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	for _, key := range keys {
		state.Cooling[key] = q.deadlines[key]
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = atomicfile.Write(q.path, raw, 0o600)
}
