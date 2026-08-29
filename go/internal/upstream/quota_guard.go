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
type Guard struct {
	options GuardOptions

	mu        sync.Mutex
	loaded    bool
	state     State
	writeMu   sync.Mutex // serializes state-file writes, mirroring the TS write chain
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
	state, err := g.load()
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
	return g.mutate(func(state *State) {
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
	})
}

// Activate records the account that successfully served a request.
func (g *Guard) Activate(account string) error {
	if !g.options.Enabled || account == "" {
		return nil
	}
	state, err := g.load()
	if err != nil {
		return err
	}
	if state.ActiveAccount == account {
		return nil
	}
	return g.mutate(func(state *State) {
		state.ActiveAccount = account
	})
}

// load returns the cached state after pruning expired cooldowns.
func (g *Guard) load() (State, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.loaded {
		state, err := ReadState(g.options.StatePath)
		if err != nil {
			return State{}, err
		}
		g.state = state
		g.loaded = true
	}
	now := g.nowMillis()
	for account, until := range g.state.Cooling {
		if until <= now {
			delete(g.state.Cooling, account)
		}
	}
	return g.state, nil
}

// mutate applies one change under the write chain and persists it.
func (g *Guard) mutate(change func(*State)) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	state, err := g.load()
	if err != nil {
		return err
	}
	change(&state)
	g.mu.Lock()
	g.state = state
	g.loaded = true
	g.mu.Unlock()
	return WriteState(g.options.StatePath, state)
}

func newEventID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return strconv.FormatInt(time.Now().UnixNano()/1e6, 36) + "-" + hex.EncodeToString(raw)[:4]
}

func trimSpace(s string) string { return strings.TrimSpace(s) }
