// Package proxy ports the ProxyService essentials (src/core/proxy-service.ts,
// lease-store.ts) and a real in-process builtin proxy server.
//
// Security model (PROXY-01 / ADR 0006):
//   - loopback bind only (assertLoopbackHost);
//   - every credentialed route requires the per-instance secret;
//   - constant-time token comparison;
//   - /health echoes the instance id without auth (ownership proof).
//
// Unlike the TS implementation (detached node children), the Go proxy runs
// in-process: ownership is trivially provable and shutdown is deterministic.
package proxy

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// LoopbackHosts mirrors assertLoopbackHost's accepted set.
var loopbackHosts = map[string]bool{
	"127.0.0.1": true,
	"::1":       true,
	"localhost": true,
}

// AssertLoopbackHost refuses any non-loopback listen host.
func AssertLoopbackHost(host string) error {
	normalized := strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	if !loopbackHosts[normalized] {
		return anyerr.Newf(
			fmt.Sprintf("Refusing to bind %q — local proxies are loopback-only.", host),
			"PROXY_UNSAFE_HOST")
	}
	return nil
}

// NewToken generates a per-instance secret: 32 random bytes, hex encoded
// (randomBytes(32).toString('hex') in TS).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// MustNewToken is the panic-on-failure helper for tests and non-critical paths.
func MustNewToken() string {
	tok, err := NewToken()
	if err != nil {
		panic(err)
	}
	return tok
}

// NewInstanceID mirrors instanceId = randomUUID().
func NewInstanceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const h = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, h[v>>4], h[v&0x0f])
	}
	return string(out), nil
}

// MustNewInstanceID panics on failure; for tests.
func MustNewInstanceID() string {
	id, err := NewInstanceID()
	if err != nil {
		panic(err)
	}
	return id
}

// SafeEqual compares secrets in constant time; an empty expected value fails
// closed.
func SafeEqual(expected, provided string) bool {
	if expected == "" {
		return false
	}
	a := []byte(expected)
	b := []byte(provided)
	if len(a) != len(b) {
		// Still burn comparable time to avoid a length oracle.
		subtle.ConstantTimeCompare(a, a)
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// bearerToken extracts Authorization: Bearer <token> or x-api-key.
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):])
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

// requireProxyAuth mirrors requireProxyAuth in src/providers/proxy-shared.ts:
// 401 JSON on missing/incorrect credentials, fail-closed on empty secret.
func requireProxyAuth(w http.ResponseWriter, r *http.Request, expectedToken string) bool {
	if !SafeEqual(expectedToken, bearerToken(r)) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid proxy credentials"}}`))
		return false
	}
	return true
}

// Server is one running builtin proxy instance.
type Server struct {
	ProviderID   string
	AccountName  string
	InstanceID   string
	Token        string
	Endpoint     string // http://127.0.0.1:<port>
	Upstream     *url.URL
	UpstreamAuth string // credential injected into forwarded requests
	// Upstream auth presentation; empty = Authorization: Bearer.
	UpstreamAuthHeader      string
	UpstreamAuthValuePrefix string
	// UpstreamAuthProvider, when set, supplies the live credential per request
	// (OIDC/OAuth refresh) and overrides UpstreamAuth.
	UpstreamAuthProvider func(ctx context.Context) (string, error)
	// UpstreamExtraHeaders are injected into every forwarded request.
	UpstreamExtraHeaders []ExtraHeader
	// TranslateAnthropic: upstream is OpenAI-only, so /v1/messages requests
	// are translated into chat completions (body + response + SSE stream).
	TranslateAnthropic bool
	// TranslateGemini: upstream is Google Gemini (generateContent), so
	// /v1/chat/completions, /v1/responses and /v1/messages are translated to
	// Gemini and the response translated back. See Upstream.TranslateGemini.
	TranslateGemini bool
	// Gemini holds the Gemini-specific egress facts (api-key vs OAuth mode).
	Gemini *GeminiTarget
	// Anthropic: optional native /v1/messages target (vendor /anthropic base).
	Anthropic *UpstreamTarget

	srv   *http.Server
	errCh chan error
	// Deliberately no translateAnthropic mirror: the live flag is read from the
	// Upstream that upstreamValue() composes. A cached copy next to the field it
	// caches is how a future reader ends up trusting the wrong one.
	clientOnce sync.Once
	client     *http.Client
}

// injectUpstreamAuth applies the configured scheme, never forwarding the
// proxy token.
func (s *Server) injectUpstreamAuth(out *http.Request) {
	injectUpstreamAuthFields(out.Context(), out, &Upstream{
		BaseURL:         s.Upstream,
		Auth:            s.UpstreamAuth,
		AuthHeader:      s.UpstreamAuthHeader,
		AuthValuePrefix: s.UpstreamAuthValuePrefix,
		AuthProvider:    s.UpstreamAuthProvider,
		ExtraHeaders:    s.UpstreamExtraHeaders,
	})
}

// Handler builds the auth + forwarding pipeline. Health stays unauthenticated
// and echoes the instance id so callers can prove the process is ours; every
// other route requires the per-instance secret (PROXY-01).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"ok":true,"instanceId":%q,"pid":%d,"provider":%q,"account":%q}`,
			s.InstanceID, ownPID(), s.ProviderID, s.AccountName)))
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireProxyAuth(w, r, s.Token) {
			return
		}
		Serve(w, r, s.upstreamValue())
	}))
	return mux
}

// upstreamValue composes the single egress description consumed by Serve.
// It must carry AuthProvider + ExtraHeaders — buildServer sets them on the
// Server, and the per-request path needs them to refresh short-lived tokens
// and inject vendor headers. A cached copy of any of these next to the field
// it caches is how a future reader ends up trusting the wrong one, so every
// egress fact lives only in the Upstream upstreamValue() returns.
func (s *Server) upstreamValue() *Upstream {
	return &Upstream{
		BaseURL:            s.Upstream,
		Auth:               s.UpstreamAuth,
		AuthHeader:         s.UpstreamAuthHeader,
		AuthValuePrefix:    s.UpstreamAuthValuePrefix,
		AuthProvider:       s.UpstreamAuthProvider,
		ExtraHeaders:       s.UpstreamExtraHeaders,
		Anthropic:          s.Anthropic,
		TranslateAnthropic: s.TranslateAnthropic,
		TranslateGemini:    s.TranslateGemini,
		Gemini:             s.Gemini,
	}
}

func writeJSONError(w http.ResponseWriter, status int, typ, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload, _ := json.Marshal(map[string]interface{}{
		"error": map[string]string{"type": typ, "message": message},
	})
	_, _ = w.Write(payload)
}

func (s *Server) httpClient() *http.Client {
	s.clientOnce.Do(func() {
		if s.client == nil {
			s.client = &http.Client{Timeout: 10 * time.Minute}
		}
	})
	return s.client
}

// Listen binds loopback and starts serving. The listener reserves the port
// before Serve so health checks can race safely.
func (s *Server) Listen(host string, port int) error {
	if err := AssertLoopbackHost(host); err != nil {
		return err
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return anyerr.Wrapf(err, "listen "+addr, "PROXY_START_FAILED")
	}
	s.Endpoint = fmt.Sprintf("http://%s:%d", normalizedLoopback(host), ln.Addr().(*net.TCPAddr).Port)
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.errCh = make(chan error, 1)
	go func() { s.errCh <- s.srv.Serve(ln) }()
	return nil
}

// WaitReady polls /health until it answers or times out.
func (s *Server) WaitReady(timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(s.Endpoint + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("health status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		return anyerr.Newf("proxy did not become healthy: "+lastErr.Error(), "PROXY_START_FAILED")
	}
	return anyerr.Newf("proxy did not become healthy", "PROXY_START_FAILED")
}

// Close stops serving. Idempotent.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// Err reports a serve failure (accept loop crash).
func (s *Server) Err() <-chan error { return s.errCh }

func normalizedLoopback(host string) string {
	n := strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	if n == "localhost" || n == "::1" {
		return "127.0.0.1"
	}
	return n
}

// PickFreePort asks the kernel for a free loopback port.
func PickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// registry tracks running servers keyed by provider/account.
type registry struct {
	mu sync.Mutex
	m  map[string]*Server
}

func key(providerID, accountName string) string { return providerID + "\x00" + accountName }

func newRegistry() *registry { return &registry{m: map[string]*Server{}} }

func (r *registry) get(providerID, accountName string) (*Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[key(providerID, accountName)]
	return s, ok
}

func (r *registry) put(providerID, accountName string, s *Server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[key(providerID, accountName)] = s
}

func (r *registry) remove(providerID, accountName string) (*Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[key(providerID, accountName)]
	if ok {
		delete(r.m, key(providerID, accountName))
	}
	return s, ok
}

// removeAll drains the registry for shutdown; callers close each server.
func (r *registry) removeAll() []*Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Server, 0, len(r.m))
	for k, s := range r.m {
		out = append(out, s)
		delete(r.m, k)
	}
	return out
}
