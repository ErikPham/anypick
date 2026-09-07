// Package observe records in-process request metrics for the loopback serving
// layer: the single-account proxy, the pool ring, the hub, and a pool source
// reached through the hub.
//
// Why this exists: before it, the only signal any of those paths produced was
// one line in proxy.log, and for a long time the pool path produced nothing at
// all. Every operational threshold in this codebase — how long a member may
// hang, how much concurrency a client may open, when a cooldown is justified —
// was therefore a guess. These counters are what turn those guesses into
// measurements.
//
// What this package must never see: a credential. Begin takes a route name and
// nothing else — no *http.Request, no headers, no body — so a bearer token
// cannot reach a counter by construction rather than by review (ADR 0006).
// Snapshots are served on the unauthenticated /health endpoint, which is only
// ever bound to loopback, and they carry counts and durations only.
//
// Counters are per process. A detached proxy child is a different process from
// the desktop service, so these numbers are deliberately exposed on each
// server's own /health rather than folded into the desktop snapshot, which
// would silently report another process's counters as its own.
package observe

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Route names are fixed strings, never derived from a request. Deriving them
// would let a caller mint unbounded counter rows with a made-up model id, and
// the map has no eviction.
const (
	// RouteSingle is one account's proxy: proxy.Server.
	RouteSingle = "proxy.single"
	// RoutePool is a standalone credential ring: PoolServer.Listener.
	RoutePool = "proxy.pool"
	// RoutePoolHub is the same ring reached through the hub's route token.
	// It is a separate row on purpose: folding it into RoutePool would make
	// one process's counters depend on how a caller happened to reach it.
	RoutePoolHub = "proxy.pool.hub"
	// RouteHub is the Proxy Hub's own model-routed listener.
	RouteHub = "hub"
)

// Class is a coarse outcome bucket. It exists so "the vendor said no" and
// "we broke" never land in the same number: the first is a failover input, the
// second is a bug.
type Class string

const (
	// ClassOK is a request that reached an upstream and came back 2xx/3xx.
	ClassOK Class = "ok"
	// ClassQuota is a quota-class upstream refusal: the input to failover.
	ClassQuota Class = "quota"
	// ClassTransient is an upstream failure that may succeed on retry.
	ClassTransient Class = "transient"
	// ClassClient is a rejection this proxy produced itself, before egress:
	// bad token, body too large, concurrency ceiling.
	ClassClient Class = "client"
	// ClassError is everything else, which after this list is "we broke".
	ClassError Class = "error"
)

// Classify maps a committed status onto a class. quotaClass overrides the
// status reading for 5xx-shaped vendor bodies that carry a quota meaning —
// the ring already computes that, and losing it here would make the failover
// counter and the quota counter disagree.
func Classify(status int, quotaClass bool) Class {
	switch {
	case quotaClass:
		return ClassQuota
	case status >= 200 && status < 400:
		return ClassOK
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		status == http.StatusRequestEntityTooLarge,
		status == http.StatusServiceUnavailable:
		return ClassClient
	case status == http.StatusTooManyRequests, status == http.StatusPaymentRequired:
		return ClassQuota
	case status >= 500:
		return ClassTransient
	default:
		return ClassError
	}
}

// Stats is a point-in-time copy of one route's counters. It is value copy,
// not a live view: callers must not be able to mutate what the server is
// counting.
type Stats struct {
	InFlight     int64            `json:"inFlight"`
	Started      int64            `json:"started"`
	Completed    int64            `json:"completed"`
	Failovers    int64            `json:"failovers"`
	LatencyMsSum int64            `json:"latencyMsSum"`
	LatencyMsMax int64            `json:"latencyMsMax"`
	StreamMsSum  int64            `json:"streamMsSum"`
	ByClass      map[string]int64 `json:"byClass"`
	ByStatus     map[string]int64 `json:"byStatus"`
}

// AverageLatencyMs is 0 when nothing has completed, not NaN and not a panic.
func (s Stats) AverageLatencyMs() int64 {
	if s.Completed == 0 {
		return 0
	}
	return s.LatencyMsSum / s.Completed
}

type routeCounters struct {
	mu sync.Mutex
	Stats
}

// Registry owns the counters for every route in this process.
type Registry struct {
	mu     sync.Mutex
	routes map[string]*routeCounters
}

// Default is the registry every serving path reports into. One per process,
// which is exactly the scope the counters are true for.
var Default = New()

func New() *Registry { return &Registry{routes: map[string]*routeCounters{}} }

func (r *Registry) route(name string) *routeCounters {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.routes[name]
	if c == nil {
		c = &routeCounters{}
		c.ByClass = map[string]int64{}
		c.ByStatus = map[string]int64{}
		r.routes[name] = c
	}
	return c
}

// Request is one in-flight request's handle. Every path must call End exactly
// once; the handle is safe to drop on a panic path because nothing but the
// gauge is lost and the gauge is corrected by End.
type Request struct {
	reg    *Registry
	counts *routeCounters
	start  time.Time
	ended  bool
}

// Begin starts timing a request on one route. Passing an empty route name is a
// silent no-op rather than a panic: metrics must never be the reason a proxy
// stops serving.
func (r *Registry) Begin(route string) *Request {
	if r == nil || route == "" {
		return &Request{}
	}
	c := r.route(route)
	c.mu.Lock()
	c.InFlight++
	c.Started++
	c.mu.Unlock()
	return &Request{reg: r, counts: c, start: time.Now()}
}

// Failover records one move to the next member in a ring.
func (req *Request) Failover() {
	if req == nil || req.counts == nil {
		return
	}
	req.counts.mu.Lock()
	req.counts.Failovers++
	req.counts.mu.Unlock()
}

// Stream adds the duration of a response that had already committed when it
// finished. Keep it separate from End's latency: averaging a selection
// timeout together with a ten-minute stream tells you nothing about either.
func (req *Request) Stream(d time.Duration) {
	if req == nil || req.counts == nil {
		return
	}
	req.counts.mu.Lock()
	req.counts.StreamMsSum += d.Milliseconds()
	req.counts.mu.Unlock()
}

// End closes the request. It is idempotent: a path that can call it twice is
// safer than a gauge that can drift upward forever.
func (req *Request) End(status int, class Class) {
	if req == nil || req.counts == nil || req.ended {
		return
	}
	req.ended = true
	elapsed := time.Since(req.start).Milliseconds()
	statusKey := strconv.Itoa(status)
	req.counts.mu.Lock()
	defer req.counts.mu.Unlock()
	req.counts.InFlight--
	req.counts.Completed++
	req.counts.LatencyMsSum += elapsed
	if elapsed > req.counts.LatencyMsMax {
		req.counts.LatencyMsMax = elapsed
	}
	req.counts.ByClass[string(class)]++
	req.counts.ByStatus[statusKey]++
}

// Snapshot copies every route's counters. The maps are fresh so a caller
// marshalling this to JSON cannot race the next request.
func (r *Registry) Snapshot() map[string]Stats {
	if r == nil {
		return map[string]Stats{}
	}
	r.mu.Lock()
	names := make([]string, 0, len(r.routes))
	for name := range r.routes {
		names = append(names, name)
	}
	routes := make([]*routeCounters, 0, len(names))
	for _, name := range names {
		routes = append(routes, r.routes[name])
	}
	r.mu.Unlock()

	out := make(map[string]Stats, len(names))
	for i, name := range names {
		c := routes[i]
		c.mu.Lock()
		st := c.Stats
		st.ByClass = copyCounts(c.ByClass)
		st.ByStatus = copyCounts(c.ByStatus)
		c.mu.Unlock()
		out[name] = st
	}
	return out
}

func copyCounts(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// HealthCounters renders every route's counters as JSON for a /health body.
// It is served unauthenticated, which is only defensible because this package
// never accepts a request: the snapshot holds counts and milliseconds, and
// there is no code path by which a credential could reach it (ADR 0006).
//
// It never fails: on a marshal error the counters are omitted rather than the
// health endpoint returning a broken body.
func HealthCounters() json.RawMessage {
	raw, err := json.Marshal(Default.Snapshot())
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ---- concurrency ceiling (design D6) ----

// DefaultMaxInFlight is the per-listener concurrency ceiling. A loopback proxy
// serving one developer does not need hundreds of streams, and a client that
// opens that many is either broken or an agent fanning out deliberately —
// either way it must degrade predictably instead of draining file descriptors
// until the process dies.
const DefaultMaxInFlight = 256

// Limiter is a queue-free semaphore: a request that arrives when the ceiling
// is reached is refused immediately, not parked. Parking would turn a burst
// into latency nobody asked for, and an unbounded queue is just a slower
// version of the crash this prevents.
type Limiter struct {
	sem chan struct{}
}

func NewLimiter(max int) *Limiter {
	if max <= 0 {
		max = DefaultMaxInFlight
	}
	return &Limiter{sem: make(chan struct{}, max)}
}

// DefaultLimiter is the process-wide ceiling. One limiter shared by every
// listener in the process, not one per listener: N listeners each allowed 256
// streams is 256·N, which is not a ceiling.
var DefaultLimiter = NewLimiter(DefaultMaxInFlight)

// Handler refuses the request with 503 + Retry-After when the ceiling is
// reached. Retry-After is what makes the refusal actionable: without it a
// well-behaved client has no idea whether to back off or give up.
//
// The refusal is counted on the route it refused. A ceiling that silently
// drops requests is how an operator ends up debugging the vendor for a
// refusal this process made.
func (l *Limiter) Handler(route string, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case l.sem <- struct{}{}:
			defer func() { <-l.sem }()
			next.ServeHTTP(w, r)
		default:
			req := Default.Begin(route)
			req.End(http.StatusServiceUnavailable, ClassClient)
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error",` +
				`"message":"too many concurrent requests"}}`))
		}
	})
}

// InFlight returns how many requests the limiter is currently holding, so a
// test can observe the ceiling without racing the handler.
func (l *Limiter) InFlight() int { return len(l.sem) }
