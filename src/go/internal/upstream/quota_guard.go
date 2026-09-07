// QuotaGuard ports the QuotaGuard class from src/providers/quota-guard.ts:
// credential-ring ordering with per-account cooldowns persisted across proxy
// restarts. It only accepts saved account names supplied by the pool and never
// writes credential material.
package upstream

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GuardOptions mirrors QuotaGuardOptions.
type GuardOptions struct {
	Enabled      bool
	CooldownMs   int64
	AccountNames []string
	StatePath    string
	ProviderID   string
}

// Candidate is anything carrying an optional saved-account name.
type Candidate interface {
	GetAccountName() string
}

// Guard is safe for concurrent use by the proxy's request handlers.
//
// Concurrency contract: g.state is owned exclusively by g.mu, and the live map
// and slice headers it holds are NEVER handed to a caller. loadLocked returns a
// deep copy. Copying a State by value would still share the Cooling map, and a
// concurrent read of that map against a cooldown write is a fatal runtime error
// rather than a recoverable panic — so the copy is explicit, not incidental.
type Guard struct {
	options GuardOptions

	mu        sync.Mutex
	loaded    bool
	state     State
	nowMillis func() int64
}

func NewGuard(options GuardOptions) *Guard {
	if options.CooldownMs <= 0 {
		options.CooldownMs = 60 * 60_000
	}
	return &Guard{options: options, nowMillis: func() int64 { return time.Now().UnixMilli() }}
}

// SetClock overrides the time source in tests.
func (g *Guard) SetClock(now func() int64) { g.nowMillis = now }

// Enabled reports whether failover policy is active.
func (g *Guard) Enabled() bool { return g.options.Enabled }

// Ordered ports QuotaGuard.ordered: drop cooling accounts, keep the last
// active account at the head of the ring. Fewer than two candidates never
// reorders; all-cooling returns nil.
func (g *Guard) Ordered(candidates []Candidate) ([]Candidate, error) {
	if !g.options.Enabled || len(candidates) < 2 {
		return append([]Candidate(nil), candidates...), nil
	}
	g.mu.Lock()
	state, err := g.loadLocked()
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	now := g.nowMillis()
	eligible := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		account := c.GetAccountName()
		if account == "" || state.Cooling[account] <= now {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return nil, nil
	}
	if state.ActiveAccount == "" {
		return eligible, nil
	}
	activeIdx := -1
	for i, c := range eligible {
		if c.GetAccountName() == state.ActiveAccount {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		return eligible, nil
	}
	ordered := []Candidate{eligible[activeIdx]}
	ordered = append(ordered, eligible[:activeIdx]...)
	ordered = append(ordered, eligible[activeIdx+1:]...)
	return ordered, nil
}

// Exhausted ports QuotaGuard.exhausted: cool `from` down for at least one
// second (or Retry-After), promote `to`, and record an audit event.
func (g *Guard) Exhausted(from, to string, retryAfterMs *int64) error {
	if !g.options.Enabled || from == "" {
		return nil
	}
	// TS: max(1_000, retryAfterMs ?? configuredCooldown). A PRESENT value —
	// even below one second — clamps to 1s; only an absent value falls back to
	// the configured cooldown.
	cooldownMs := g.options.CooldownMs
	if retryAfterMs != nil {
		cooldownMs = *retryAfterMs
	}
	if cooldownMs < 1000 {
		cooldownMs = 1000
	}
	cooldownUntil := g.nowMillis() + cooldownMs

	g.mu.Lock()
	state, err := g.loadLocked()
	if err != nil {
		g.mu.Unlock()
		return err
	}
	// Stampede collapse. Every request in a burst observes the same member
	// exhausted and calls Exhausted. Because loadLocked prunes every entry
	// whose deadline has passed, a surviving key means the member is still
	// cooling: re-reporting it adds no information, so only the first caller
	// pays for the state write. Without this, N parallel requests against one
	// rate-limited member produce N serialized atomic file writes.
	//
	// Comparing against the newly computed deadline instead would not collapse
	// anything — nowMillis() advances between callers, so every later deadline
	// is strictly greater than the stored one.
	if _, cooling := state.Cooling[from]; cooling {
		if to == "" || state.ActiveAccount == to {
			g.mu.Unlock()
			return nil
		}
		// A different successor is still worth recording: it changes which
		// member leads the ring on the next request.
		state.ActiveAccount = to
		g.state = state
		g.loaded = true
		snapshot := cloneState(state)
		g.mu.Unlock()
		return WriteState(g.options.StatePath, snapshot)
	}
	state.Cooling[from] = cooldownUntil
	if to != "" {
		state.ActiveAccount = to
	}
	ev := Event{
		ID:            newEventID(),
		CreatedAt:     time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		ProviderID:    g.options.ProviderID,
		From:          from,
		To:            to,
		CooldownUntil: cooldownUntil,
	}
	state.Events = append([]Event{ev}, state.Events...)
	if len(state.Events) > MaxEvents {
		state.Events = state.Events[:MaxEvents]
	}
	g.state = state
	g.loaded = true
	snapshot := cloneState(state)
	g.mu.Unlock()
	// Persist outside the lock: WriteState is a synchronous atomic file write,
	// and holding mu across it queues every concurrent request behind disk I/O.
	return WriteState(g.options.StatePath, snapshot)
}

// Activate records the account that successfully served a request.
func (g *Guard) Activate(account string) error {
	if !g.options.Enabled || account == "" {
		return nil
	}
	g.mu.Lock()
	state, err := g.loadLocked()
	if err != nil {
		g.mu.Unlock()
		return err
	}
	if state.ActiveAccount == account {
		g.mu.Unlock()
		return nil
	}
	state.ActiveAccount = account
	g.state = state
	g.loaded = true
	snapshot := cloneState(state)
	g.mu.Unlock()
	return WriteState(g.options.StatePath, snapshot)
}

// loadLocked returns a deep copy of the cached state after pruning expired
// cooldowns. Callers must hold g.mu.
func (g *Guard) loadLocked() (State, error) {
	if !g.loaded {
		state, err := ReadState(g.options.StatePath)
		if err != nil {
			return State{}, err
		}
		g.state = state
		g.loaded = true
	}
	if g.state.Cooling == nil {
		g.state.Cooling = map[string]int64{}
	}
	now := g.nowMillis()
	for account, until := range g.state.Cooling {
		if until <= now {
			delete(g.state.Cooling, account)
		}
	}
	return cloneState(g.state), nil
}

// cloneState copies the headers a State carries by value. A plain struct
// assignment would leave the Cooling map reachable from two owners at once,
// and a read racing a cooldown write on that map is a fatal runtime error.
func cloneState(s State) State {
	out := s
	if s.Cooling != nil {
		out.Cooling = make(map[string]int64, len(s.Cooling))
		for k, v := range s.Cooling {
			out.Cooling[k] = v
		}
	}
	if s.Events != nil {
		out.Events = make([]Event, len(s.Events))
		copy(out.Events, s.Events)
	}
	return out
}

func newEventID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return strconv.FormatInt(time.Now().UnixNano()/1e6, 36) + "-" + hex.EncodeToString(raw)[:4]
}

func trimSpace(s string) string { return strings.TrimSpace(s) }
