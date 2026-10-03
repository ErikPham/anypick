// Package upstream ports src/providers/upstream-policy.ts and
// src/providers/quota-guard.ts: failure classification, retry-after parsing,
// cooldown bookkeeping, and the multi-account Quota Guard ordering policy.
// These are runtime-only policies: never account settings, never credentials.
package upstream

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/atomicfile"
)

// FailureKind mirrors UpstreamFailureKind.
type FailureKind string

const (
	KindRateLimit FailureKind = "rate_limit"
	KindAuth      FailureKind = "auth"
	KindTransient FailureKind = "transient"
	KindServer    FailureKind = "server"
	KindPermanent FailureKind = "permanent"
)

// LimitScope mirrors LimitScope.
type LimitScope string

const (
	ScopeIP         LimitScope = "ip"
	ScopeCredential LimitScope = "credential"
	ScopeModel      LimitScope = "model"
	ScopeUnknown    LimitScope = "unknown"
)

type ClassifiedFailure struct {
	Kind         FailureKind `json:"kind"`
	Status       int         `json:"status,omitempty"`
	Scope        LimitScope  `json:"scope"`
	RetryAfterMs *int64      `json:"retryAfterMs,omitempty"`
	Message      string      `json:"message,omitempty"`
}

var (
	balanceRe        = regexp.MustCompile(`(?i)insufficient[_\s-]*(?:account[_\s-]*)?balance|account[_\s-]*balance[^.]{0,80}insufficient|balance[^.]{0,80}(?:depleted|exhausted)`)
	ipOrMinuteRe     = regexp.MustCompile(`(?i)freeusagelimit|free usage|\bip(?: address)?\b|per[-\s]?minute|rate limit`)
	exhaustedRe      = regexp.MustCompile(`(?i)resource[_\s-]*exhausted`)
	keyAttributionRe = regexp.MustCompile(`(?i)(?:api[_\s-]*key|credential|project|account|billing)[^.]{0,100}(?:quota|balance|exhausted|limit)|(?:quota|balance)[^.]{0,100}(?:exhausted|depleted|exceeded|insufficient)`)
	scopeIPRe        = regexp.MustCompile(`(?i)freeusagelimit|free usage|\bip\b`)
)

// IsInsufficientBalanceFailure ports isInsufficientBalanceFailure: billing
// exhaustion is credential-scoped, unlike an IP/model rate limit. Deliberately
// narrow — a generic 403 must not disable a healthy account.
func IsInsufficientBalanceFailure(status int, bodyPreview string) bool {
	if status != 402 && status != 403 {
		return false
	}
	text := truncate(bodyPreview, 2000)
	return balanceRe.MatchString(text)
}

// IsCredentialQuotaFailure ports isCredentialQuotaFailure: true only when the
// vendor response explicitly attributes exhaustion to an API key, project,
// account, billing balance, or quota allocation.
func IsCredentialQuotaFailure(status int, headerValue func(string) string, bodyPreview string) bool {
	text := headerValue("x-goog-api-client") + " " + truncate(bodyPreview, 2000)
	if status != 429 && !IsInsufficientBalanceFailure(status, bodyPreview) {
		return false
	}
	if ipOrMinuteRe.MatchString(text) {
		return false
	}
	return exhaustedRe.MatchString(text) || keyAttributionRe.MatchString(text)
}

// MatchesQuotaErrorPattern checks provider-declared quotaErrorPatterns regexes
// against the response body. Invalid patterns are skipped.
func MatchesQuotaErrorPattern(patterns []string, body string) bool {
	if len(patterns) == 0 {
		return false
	}
	preview := body
	if len(preview) > 4000 {
		preview = preview[:4000]
	}
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(preview) {
			return true
		}
	}
	return false
}

// ParseRetryAfter ports parseRetryAfter (delta-seconds or HTTP-date).
func ParseRetryAfter(value string, now time.Time) *int64 {
	raw := trimSpace(value)
	if raw == "" {
		return nil
	}
	if seconds, ok := parseFloat(raw); ok {
		if seconds >= 0 {
			ms := int64(seconds * 1000)
			return &ms
		}
		return nil
	}
	if t, err := httpDateParse(raw); err == nil && !t.Before(now) {
		ms := t.Sub(now).Milliseconds()
		return &ms
	}
	return nil
}

// ClassifyUpstreamFailure ports classifyUpstreamFailure.
func ClassifyUpstreamFailure(status int, headerValue func(string) string, bodyPreview string, now time.Time) ClassifiedFailure {
	text := truncate(bodyPreview, 2000)
	switch {
	case status == 429:
		scope := ScopeUnknown
		if scopeIPRe.MatchString(text) {
			scope = ScopeIP
		}
		return ClassifiedFailure{Kind: KindRateLimit, Status: status, Scope: scope,
			RetryAfterMs: ParseRetryAfter(headerValue("retry-after"), now), Message: text}
	case status == 401 || status == 403:
		return ClassifiedFailure{Kind: KindAuth, Status: status, Scope: ScopeCredential, Message: text}
	case status == 408 || status == 502 || status == 503 || status == 504:
		return ClassifiedFailure{Kind: KindTransient, Status: status, Scope: ScopeUnknown, Message: text}
	case status >= 500:
		return ClassifiedFailure{Kind: KindServer, Status: status, Scope: ScopeUnknown, Message: text}
	default:
		return ClassifiedFailure{Kind: KindPermanent, Status: status, Scope: ScopeUnknown, Message: text}
	}
}

// CooldownRegistry mirrors CooldownRegistry: in-memory per-key deadlines.
type CooldownRegistry struct {
	mu        sync.Mutex
	deadlines map[string]time.Time
	now       func() time.Time
}

func NewCooldownRegistry(now func() time.Time) *CooldownRegistry {
	if now == nil {
		now = time.Now
	}
	return &CooldownRegistry{deadlines: map[string]time.Time{}, now: now}
}

func (c *CooldownRegistry) Set(key string, delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if delay > 0 {
		c.deadlines[key] = c.now().Add(delay)
	}
}

func (c *CooldownRegistry) Remaining(key string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline, ok := c.deadlines[key]
	if !ok {
		return 0
	}
	remaining := deadline.Sub(c.now())
	if remaining <= 0 {
		delete(c.deadlines, key)
		return 0
	}
	return remaining
}

func (c *CooldownRegistry) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deadlines)
}

// MaxEvents mirrors MAX_EVENTS.
const MaxEvents = 100

// Event mirrors QuotaGuardEvent.
type Event struct {
	ID            string `json:"id"`
	CreatedAt     string `json:"createdAt"`
	ProviderID    string `json:"providerId,omitempty"`
	From          string `json:"from"`
	To            string `json:"to,omitempty"`
	CooldownUntil int64  `json:"cooldownUntil"`
}

// State mirrors QuotaGuardState. The shape is cross-runtime: TypeScript reads
// the same file, so field names and the version literal are contractual.
type State struct {
	Version       int              `json:"version"`
	ActiveAccount string           `json:"activeAccount,omitempty"`
	Cooling       map[string]int64 `json:"cooling"`
	Events        []Event          `json:"events"`
}

// EmptyState returns {version:1, cooling:{}, events:[]}.
func EmptyState() State {
	return State{Version: 1, Cooling: map[string]int64{}, Events: []Event{}}
}

// ReadState ports readQuotaGuardState: absent paths yield the empty state and
// corrupt files degrade instead of preventing the proxy from serving requests.
func ReadState(path string) (State, error) {
	if path == "" {
		return EmptyState(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return EmptyState(), nil
	}
	return NormalizeState([]byte(raw)), nil
}

// NormalizeState ports normalizeQuotaGuardState with its strict field filters:
// a non-object document yields the empty state; cooling entries keep only
// account names ≤128 chars with positive numeric deadlines; events must carry
// id/createdAt/from/cooldownUntil and are capped at MaxEvents.
func NormalizeState(raw []byte) State {
	var top map[string]json.RawMessage
	state := EmptyState()
	if json.Unmarshal(raw, &top) != nil {
		return EmptyState()
	}
	if v, ok := top["activeAccount"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			state.ActiveAccount = s
		}
	}
	if v, ok := top["cooling"]; ok {
		var cooling map[string]json.Number
		if json.Unmarshal(v, &cooling) == nil {
			for account, until := range cooling {
				if len(account) <= 128 {
					if n, err := until.Int64(); err == nil && n > 0 {
						state.Cooling[account] = n
					}
				}
			}
		}
	}
	if v, ok := top["events"]; ok {
		var events []json.RawMessage
		if json.Unmarshal(v, &events) == nil {
			for _, rawEvent := range events {
				var ev Event
				if json.Unmarshal(rawEvent, &ev) != nil {
					continue
				}
				if ev.ID == "" || ev.CreatedAt == "" || ev.From == "" || ev.CooldownUntil == 0 {
					continue
				}
				state.Events = append(state.Events, ev)
				if len(state.Events) >= MaxEvents {
					break
				}
			}
		}
	}
	return state
}

// WriteState atomically persists the audit file (owner-only, secret-free).
func WriteState(path string, state State) error {
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, raw, 0o600)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func parseFloat(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(trimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func httpDateParse(value string) (time.Time, error) {
	return http.ParseTime(value)
}
