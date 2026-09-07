package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/observe"
	"github.com/outof0/anypick/go/internal/proxy"
)

// UpstreamResolver resolves a source into forwarding parameters.
type UpstreamResolver interface {
	ResolveUpstream(ctx context.Context, providerID, account string) (*proxy.Upstream, error)
}

// PoolRingResolver resolves a pool-backed source into its credential ring —
// the same guard-ordered, quota-class failover pipeline a standalone pool
// serves (src/core/proxy-hub-server.ts hands every enabled pool member to the
// backend ring; TS hub sources are account OR pool refs).
type PoolRingResolver interface {
	ResolvePoolRing(ctx context.Context, providerID string) (*proxy.PoolServer, error)
}

// Server is one running hub instance.
type Server struct {
	Config   Config
	Instance string
	Store    *Store
	Resolver UpstreamResolver
	// PoolRings serves pool-backed sources; nil makes them fail closed with a
	// clear upstream error instead of a panic.
	PoolRings PoolRingResolver

	mu      sync.Mutex
	httpSrv interface {
		Shutdown() error
	}
	endpoint string
	errCh    chan error
}

// Start binds the hub on loopback and begins serving. Health stays
// unauthenticated; every model route requires its per-route token.
func (s *Server) Start() error {
	host := s.Config.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if err := proxy.AssertLoopbackHost(host); err != nil {
		return err
	}
	port := s.Config.Port

	mux := http.NewServeMux()
	if s.Instance == "" {
		instance, err := proxy.NewInstanceID()
		if err != nil {
			return err
		}
		s.Instance = instance
	} // else: the detached parent seeded our instance so /health echoes the
	// recorded ownership id (ADR 0003 §3).

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"instanceId":%q,"hub":%q,"counters":%s}`,
			s.Instance, s.Config.Name, observe.HealthCounters())
	})
	mux.Handle("/", observe.DefaultLimiter.Handler(observe.RouteHub,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := proxy.NewStatusRecorder(w)
			req := observe.Default.Begin(observe.RouteHub)
			// A closure, not `defer req.End(rec.Status(), ...)`: defer
			// evaluates its arguments now, which would file every request
			// under the recorder's initial 200 regardless of what the route
			// answered.
			defer func() {
				req.End(rec.Status(), observe.Classify(rec.Status(), false))
			}()
			s.serveRoute(rec, r)
		})))

	ln, err := listenLoopback(host, port)
	if err != nil {
		return err
	}
	// Publish every runtime field under mu: Endpoint() and Stop() read them
	// from other goroutines, and the accept loop publishes its failure through
	// errCh instead of discarding it. A hub whose listener died used to stay
	// "running" forever with no observable error.
	hs := proxy.ApplyServingLimits(&http.Server{Handler: mux})
	done := make(chan error, 1)
	go func() { done <- hs.Serve(ln) }()

	s.mu.Lock()
	s.endpoint = fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*tcpAddr).Port)
	s.errCh = done
	s.httpSrv = shutdownAdapter{srv: hs}
	s.mu.Unlock()
	return nil
}

// Endpoint returns http://127.0.0.1:<bound port> once started.
func (s *Server) Endpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
}

// Err reports an accept-loop failure. It returns a nil channel before Start,
// which blocks forever rather than spinning on a closed one.
func (s *Server) Err() <-chan error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errCh
}

// config returns a snapshot of the running configuration. The request handler
// reads it on every request, so it is copied under mu instead of read
// unlocked. The Sources slice is shared with the copy, so it must be treated
// as immutable once Start has returned.
func (s *Server) config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Config
}

// Stop shuts the hub down; idempotent.
//
// The lock is released before Shutdown is called. Shutdown waits for in-flight
// requests, and every request calls config() under that same lock — holding it
// here would stall all of them until the shutdown context expires, and would
// block Endpoint() and Err() behind them.
func (s *Server) Stop() error {
	s.mu.Lock()
	srv := s.httpSrv
	s.httpSrv = nil
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown()
}

func (s *Server) serveRoute(w http.ResponseWriter, r *http.Request) {
	token := presentedToken(r)
	route, err := s.Store.RouteByToken(token)
	if err != nil || route == nil || !proxy.SafeEqual(route.Token, token) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid hub credentials"}}`))
		return
	}
	// Opportunistic legacy-secret migration. Authentication has already
	// succeeded; this write converges pre-HMAC rows and is skipped for rows
	// that already match. It stays out of RouteByToken so the auth read path
	// is pure. A failure is logged, never swallowed: the legacy row keeps
	// authenticating, but silence would hide a broken store.
	if merr := s.Store.MigrateRouteTokenContext(r.Context(), route.RouteID, token); merr != nil {
		log.Printf("hub: route token migration failed for %s: %v", route.RouteID, merr)
	}
	cfg := s.config()
	if !cfg.Enabled {
		writeErr(w, http.StatusConflict, "PROXY_DISABLED", "hub "+cfg.Name+" is disabled")
		return
	}

	// Which source serves this request?
	sourceIdx := defaultSourceIndex(&cfg)
	var requestedModel string
	if r.Method == http.MethodPost {
		body, readErr := proxy.ReadCappedBody(r, proxy.MaxBodyBytes)
		if errors.Is(readErr, proxy.ErrBodyTooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
				fmt.Sprintf("request body exceeds %d bytes", proxy.MaxBodyBytes))
			return
		}
		if readErr != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", "unreadable body")
			return
		}
		var probe struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &probe)
		requestedModel = probe.Model
		if idx, ok := routeSourceIndex(route, probe.Model); ok {
			sourceIdx = idx
		} else if strings.HasPrefix(requestedModel, "#") {
			// "#N" pins a source index explicitly.
			if n := parseIndex(requestedModel); n >= 0 && n < len(cfg.Sources) {
				sourceIdx = n
			} else {
				sourceIdx = -1
			}
		}
		// A model absent from the manifest falls through to the default source:
		// route.Models is a pinning table, not an allowlist. ADR 0013 §3's
		// "no first-source-wins" rule is about collisions — when two sources
		// claim the same model id, the id stays out of the catalog until the
		// user records an owner. It does not make an unlisted model an error.
		// Rebody for the upstream with any route-model mapping applied.
		rewritten := rewriteModel(body, route, requestedModel)
		r.Body = io.NopCloser(strings.NewReader(rewritten))
		r.ContentLength = int64(len(rewritten))
	}
	if sourceIdx < 0 || sourceIdx >= len(cfg.Sources) {
		if len(cfg.Sources) == 0 {
			writeErr(w, http.StatusNotFound, "NO_SOURCE", "hub has no sources")
			return
		}
		writeErr(w, http.StatusNotFound, "NO_MODEL_SOURCE",
			fmt.Sprintf("no source in this route serves model %q", requestedModel))
		return
	}
	src := cfg.Sources[sourceIdx]

	// A source whose account value is `pool:<provider>` (the refs.AccountPoolRef
	// serialization) delegates to that provider's multi-account credential ring.
	if providerID, ok := poolSourceProvider(src); ok {
		if s.PoolRings == nil {
			writeErr(w, http.StatusBadGateway, "upstream_error",
				"pool sources are not served by this hub instance")
			return
		}
		ring, rerr := s.PoolRings.ResolvePoolRing(r.Context(), providerID)
		if rerr != nil {
			writeErr(w, http.StatusBadGateway, "upstream_error", rerr.Error())
			return
		}
		// Authentication already happened above (route token); the ring adds
		// guard ordering + quota-class-only failover, verbatim passthrough else.
		ring.ServeAuthorized(w, r)
		return
	}

	up, uerr := s.Resolver.ResolveUpstream(r.Context(), src.ProviderID, src.Account)
	if uerr != nil {
		writeErr(w, http.StatusBadGateway, "upstream_error", uerr.Error())
		return
	}
	proxy.Serve(w, r, up)
}

// ---- small helpers kept dependency-light for tests ----

// poolSourceProvider reports whether the source references a provider pool and
// returns that provider id.
func poolSourceProvider(src Source) (string, bool) {
	const prefix = "pool:"
	if !strings.HasPrefix(src.Account, prefix) || len(src.Account) == len(prefix) {
		return "", false
	}
	return strings.TrimPrefix(src.Account, prefix), true
}

func routeSourceIndex(route *Route, model string) (int, bool) {
	for _, m := range route.Models {
		if m.Model == model {
			return m.SourceIndex, true
		}
	}
	return 0, false
}

func rewriteModel(body []byte, route *Route, model string) string {
	for _, m := range route.Models {
		if m.Model == model && m.UpstreamModel != "" && m.UpstreamModel != model {
			// Decode with UseNumber. Unmarshalling into interface{} turns every
			// JSON number into a float64, so re-marshalling rewrites large
			// integers that cannot survive the round trip — a proxy has no
			// business changing numbers it was only asked to relabel.
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber()
			var doc map[string]interface{}
			if dec.Decode(&doc) != nil {
				return string(body)
			}
			doc["model"] = m.UpstreamModel
			out, err := json.Marshal(doc)
			if err != nil {
				return string(body)
			}
			return string(out)
		}
	}
	return string(body)
}

func parseIndex(model string) int {
	n := 0
	for i := 1; i < len(model); i++ {
		if model[i] < '0' || model[i] > '9' {
			return -1
		}
		n = n*10 + int(model[i]-'0')
	}
	if len(model) <= 1 {
		return -1
	}
	return n
}

func defaultSourceIndex(cfg *Config) int {
	if len(cfg.Sources) == 0 {
		return -1
	}
	return 0
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload, _ := json.Marshal(map[string]interface{}{
		"error": map[string]string{"type": code, "message": message},
	})
	_, _ = w.Write(payload)
}

// ---- plumbing helpers ----

type tcpAddr = net.TCPAddr

func listenLoopback(host string, port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
}

type shutdownAdapter struct{ srv *http.Server }

func (a shutdownAdapter) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.srv.Shutdown(ctx)
}

// presentedToken mirrors the per-account server: Authorization Bearer or
// x-api-key.
func presentedToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):])
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}
