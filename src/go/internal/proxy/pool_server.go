// PoolServer ports the multi-account credential ring served by
// src/providers/compatible/backend.ts behind startPoolProxy
// (src/core/proxy-service.ts): one loopback listener, one pool token, and a
// ring of enabled member accounts resolved through the same upstream resolver
// path as single-account proxies.
//
// Failover parity is intentional: the persisted strategy (failover |
// round-robin) is recorded but NOT rotated — like TypeScript, the ring is
// always ordered-failover with active-account promotion via the QuotaGuard.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/protocol"
	"github.com/outof0/anypick/go/internal/upstream"
)

// poolCandidate adapts a member account name to the QuotaGuard ordering input.
type poolCandidate struct{ account string }

func (c poolCandidate) GetAccountName() string { return c.account }

// PoolServer is one running multi-account pool instance. Members are enabled
// account names in ring order; upstreams resolve lazily per request so a hot
// member snapshot change cannot strand the server.
type PoolServer struct {
	ProviderID string
	InstanceID string
	Token      string
	Members    []string
	// Strategy mirrors the persisted pool row ('failover' | 'round-robin').
	// Recorded for diagnostics only — no rotation is implemented, matching TS.
	Strategy string
	Resolver UpstreamResolver
	Guard    *upstream.Guard

	endpoint string
	srv      *http.Server
	errCh    chan error

	resolvedMu sync.Mutex
	resolved   map[string]*Upstream
}

// Handler builds the auth + failover pipeline. /health stays unauthenticated
// and echoes the instance id + pid + provider so callers can prove ownership
// of the process serving this port; every other route requires the pool token.
func (s *PoolServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"instanceId":"` + s.InstanceID +
			`","pid":` + strconv.Itoa(ownPID()) + `,"provider":"` + s.ProviderID + `","account":""}`))
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unauthenticatedLiveness(r) {
			writeLiveness(w, r)
			return
		}
		if !requireProxyAuth(w, r, s.Token) {
			return
		}
		s.servePoolRequest(w, r)
	}))
	return mux
}

// ServeAuthorized handles one request whose authentication an outer listener
// already performed — the Proxy Hub verifies its own route token before
// delegating a pool-backed source here, so the pool token check is skipped.
func (s *PoolServer) ServeAuthorized(w http.ResponseWriter, r *http.Request) {
	s.servePoolRequest(w, r)
}

// servePoolRequest walks the guard-ordered member ring. Statuses pass through
// verbatim unless the failure is quota-class AND a next candidate exists AND
// the guard is enabled — exactly backend.ts fetch().
func (s *PoolServer) servePoolRequest(w http.ResponseWriter, r *http.Request) {
	// TS reuses a buffered body for every ring attempt; ReverseProxy drains
	// r.Body on the first hop, so snapshot it once and replay per member.
	var bodyBytes []byte
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		_ = r.Body.Close()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "unreadable body")
			return
		}
		bodyBytes = raw
	}
	candidates := make([]upstream.Candidate, len(s.Members))
	for i, m := range s.Members {
		candidates[i] = poolCandidate{account: m}
	}
	ordered, err := s.Guard.Ordered(candidates)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "upstream_error", "quota guard state unreadable")
		return
	}
	if len(ordered) == 0 {
		writeJSONError(w, http.StatusTooManyRequests, "rate_limit_error", "All accounts are cooling down.")
		return
	}

	for i, c := range ordered {
		account := c.GetAccountName()
		up := s.upstreamFor(r, account)
		if up == nil || up.BaseURL == nil {
			continue
		}
		if bodyBytes != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else {
			r.Body = nil
		}
		cw := &captureWriter{ResponseWriter: w}
		s.serveMember(cw, r, up)
		if cw.committed {
			_ = s.Guard.Activate(account)
			return
		}
		// TS: `if (!next || !guard.enabled || !isExhausted(...)) return last;`
		if i+1 >= len(ordered) || !s.Guard.Enabled() ||
			!isQuotaClassFailure(cw.status, cw.headerValue, cw.body.String(),
				quotaErrorPatterns(up)) {
			cw.flushCaptured()
			return
		}
		next := ordered[i+1].GetAccountName()
		retryAfter := upstream.ParseRetryAfter(cw.headerValue("Retry-After"), time.Now())
		if gerr := s.Guard.Exhausted(account, next, retryAfter); gerr != nil {
			// State-file failure must not leak credentials or kill the swap.
			_ = gerr
		}
	}
	// Every candidate lacked a usable upstream target.
	writeJSONError(w, http.StatusServiceUnavailable, "upstream_error", "No eligible pool account.")
}

// upstreamFor resolves and caches one member's egress description through the
// shared resolver path used by single-account proxies.
func (s *PoolServer) upstreamFor(_ *http.Request, account string) *Upstream {
	s.resolvedMu.Lock()
	defer s.resolvedMu.Unlock()
	if s.resolved == nil {
		s.resolved = map[string]*Upstream{}
	}
	if up, ok := s.resolved[account]; ok {
		return up
	}
	if s.Resolver == nil {
		return nil
	}
	up, err := s.Resolver.ResolveUpstream(context.Background(), s.ProviderID, account)
	if err != nil || up == nil {
		return nil
	}
	s.resolved[account] = up
	return up
}

// serveMember forwards one request to one member's upstream using the SAME
// dispatch order as Serve (native Anthropic → translated messages → plain
// passthrough), but captures non-2xx responses instead of committing them so
// the ring loop can decide whether to fail over.
func (s *PoolServer) serveMember(w *captureWriter, r *http.Request, up *Upstream) {
	isMessages := r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/messages" || r.URL.Path == "/messages")
	isGemini := r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/chat/completions" ||
			r.URL.Path == "/v1/responses" ||
			r.URL.Path == "/v1/messages")
	if isGemini && up.TranslateGemini {
		s.geminiWithCapture(w, r, up)
		return
	}
	if isMessages && up.Anthropic != nil {
		t := up.Anthropic
		auth := t.Auth
		if auth == "" {
			auth = up.Auth
		}
		header, prefix := t.AuthHeader, t.AuthValuePrefix
		if header == "" {
			header, prefix = up.AuthHeader, up.AuthValuePrefix
		}
		provider := t.AuthProvider
		if provider == nil {
			provider = up.AuthProvider
		}
		headers := append(append([]ExtraHeader(nil), t.ExtraHeaders...), up.ExtraHeaders...)
		s.reverseWithCapture(w, r, t.BaseURL, &Upstream{
			Auth: auth, AuthHeader: header, AuthValuePrefix: prefix,
			AuthProvider: provider, ExtraHeaders: headers,
		})
		return
	}
	if isMessages && up.TranslateAnthropic {
		s.translatedWithCapture(w, r, up)
		return
	}
	s.reverseWithCapture(w, r, up.BaseURL, up)
}

// geminiWithCapture ports serveGemini's manual egress with capture semantics:
// a Gemini-backed member still accepts /v1/messages + /v1/responses +
// /v1/chat/completions and translates them to generateContent, capturing
// non-2xx responses so the ring can fail over like any other member.
func (s *PoolServer) geminiWithCapture(w *captureWriter, r *http.Request, up *Upstream) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		w.commitJSON(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"unreadable body"}}`)
		return
	}
	geminiReq, resolvedModel, stream, terr := translateToGemini(up, r.Context(), r.URL.Path, body)
	if terr != nil {
		w.commitJSON(http.StatusBadGateway, jsonErrorBody("upstream_error", terr.Error()))
		return
	}
	if resolvedModel == "" {
		w.commitJSON(http.StatusBadRequest,
			jsonErrorBody("invalid_request_error", "model is required and no model could be discovered for this account"))
		return
	}
	resp, cerr := callGemini(r.Context(), up, resolvedModel, geminiReq)
	if cerr != nil {
		// Capture the upstream failure so the ring loop can decide failover.
		status, msg := geminiErrorStatusMessage(cerr)
		w.capture(status, http.Header{"Content-Type": []string{"application/json"}},
			[]byte(jsonErrorBody("proxy_error", msg)))
		return
	}
	w.commit()
	writeGeminiResponse(w, r.URL.Path, resp, resolvedModel, stream)
}

// reverseWithCapture streams committed responses straight through and buffers
// non-committed (≥400) responses into the capture writer.
func (s *PoolServer) reverseWithCapture(w *captureWriter, r *http.Request, baseURL *url.URL, up *Upstream) {
	target := *baseURL
	chatPath := ""
	var sanitized []byte
	sanitizing := false
	if up != nil && up.OpenAIPath != "" && r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/completions") {
		chatPath = strings.TrimSuffix(target.Path, "/") + up.OpenAIPath
		if up.SanitizeOpenAIBody != nil {
			if raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<20)); err == nil {
				sanitized = up.SanitizeOpenAIBody(raw)
				sanitizing = true
			}
		}
	}
	// Fail closed on credential refresh: a stale/empty token would only
	// surface as an upstream 401 and hide the real cause. Pre-resolve here
	// because Rewrite cannot return an error.
	var authHeader, authPrefix, authToken string
	if up != nil {
		var aerr error
		authHeader, authPrefix, authToken, aerr = resolveUpstreamAuth(r.Context(), up)
		if aerr != nil {
			w.capture(http.StatusBadGateway, http.Header{"Content-Type": []string{"application/json"}},
				[]byte(jsonErrorBody("proxy_error", "credential refresh failed")))
			return
		}
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			if chatPath != "" {
				pr.Out.URL.Path = chatPath
				pr.Out.URL.RawPath = ""
			}
			if sanitizing {
				pr.Out.Body = io.NopCloser(bytes.NewReader(sanitized))
				pr.Out.ContentLength = int64(len(sanitized))
				pr.Out.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(sanitized)), nil
				}
			}
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("x-api-key")
			pr.Out.Header.Del("api-key")
			pr.Out.Header.Set(authHeader, authPrefix+authToken)
			applyExtraHeaders(pr.Out, up.ExtraHeaders)
		},
		FlushInterval: 10 * time.Millisecond,
		ErrorHandler: func(flush http.ResponseWriter, _ *http.Request, _ error) {
			// Transport errors surface as a verbatim 502; transient class never
			// triggers failover.
			flush.Header().Set("Content-Type", "application/json")
			flush.WriteHeader(http.StatusBadGateway)
			_, _ = flush.Write([]byte(`{"error":{"type":"upstream_error","message":"upstream unavailable"}}`))
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode < 400 {
				w.commit()
				return nil
			}
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if err != nil {
				raw = nil
			}
			w.capture(resp.StatusCode, resp.Header, raw)
			// ReverseProxy replays resp.Body into this same uncommitted writer
			// after ModifyResponse returns; leaving the captured bytes there
			// appended every verbatim passthrough body a second time. Hand it an
			// empty body — flushCaptured replays the capture exactly once.
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			return nil
		},
	}
	rp.ServeHTTP(w, r)
}

// translatedWithCapture ports serveTranslatedMessagesWith's manual egress with
// capture semantics: OpenAI-only members still accept /v1/messages requests.
func (s *PoolServer) translatedWithCapture(w *captureWriter, r *http.Request, up *Upstream) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		w.commitJSON(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"unreadable body"}}`)
		return
	}
	translated, terr := translateRequestBody(body)
	if terr != nil {
		w.commitJSON(http.StatusBadRequest, jsonErrorBody("invalid_request_error", terr.Error()))
		return
	}
	if up.SanitizeOpenAIBody != nil {
		translated = up.SanitizeOpenAIBody(translated)
	}
	target := *up.BaseURL
	target.Path = strings.TrimSuffix(target.Path, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(),
		bytes.NewReader(translated))
	if err != nil {
		w.commitJSON(http.StatusBadGateway, `{"error":{"type":"upstream_error","message":"upstream unavailable"}}`)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if err := injectUpstreamAuthFields(r.Context(), req, up); err != nil {
		w.commitJSON(http.StatusBadGateway, jsonErrorBody("proxy_error", "credential refresh failed"))
		return
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		w.commitJSON(http.StatusBadGateway, `{"error":{"type":"upstream_error","message":"upstream unavailable"}}`)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		w.capture(resp.StatusCode, resp.Header, raw)
		return
	}
	w.commit()
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") || strings.Contains(ct, "stream") {
		_ = protocol.TranslateStreamingResponse(resp, w)
		return
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	out, terr := protocol.TranslateResponse(raw)
	if terr != nil {
		w.Header().Set("Content-Type", "application/json")
		writeJSONError(w.ResponseWriter, http.StatusBadGateway, "upstream_error", terr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func jsonErrorBody(typ, message string) string {
	payload, _ := json.Marshal(map[string]interface{}{
		"error": map[string]string{"type": typ, "message": message},
	})
	return string(payload)
}

// isQuotaClassFailure ports backend.ts isExhausted: 401/402, insufficient
// balance, explicit credential-quota attribution, or a provider-declared
// quotaErrorPatterns regex matching the body.
func isQuotaClassFailure(status int, headerValue func(string) string, bodyPreview string, patterns []string) bool {
	if status == 401 || status == 402 {
		return true
	}
	if upstream.IsInsufficientBalanceFailure(status, bodyPreview) {
		return true
	}
	if upstream.IsCredentialQuotaFailure(status, headerValue, bodyPreview) {
		return true
	}
	return matchesQuotaErrorPattern(patterns, bodyPreview)
}

// matchesQuotaErrorPattern ports the definition.quotaErrorPatterns branch of
// backend.ts isExhausted: each provider-declared regex probes the first 4000
// body bytes. Invalid patterns are skipped — the TS catalog validator rejects
// them at registration, so a bad pattern must never widen classification.
func matchesQuotaErrorPattern(patterns []string, body string) bool {
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

// quotaErrorPatterns surfaces the resolved member's provider-declared quota
// patterns; nil-safe for members without a usable upstream.
func quotaErrorPatterns(up *Upstream) []string {
	if up == nil {
		return nil
	}
	return up.QuotaErrorPatterns
}

// captureWriter lets ReverseProxy stream committed responses while silently
// buffering responses the ring may replace with a later member's answer.
// Until a response commits, Header() hands out a scratch map so a failed
// member's headers can never pollute the real response.
type captureWriter struct {
	http.ResponseWriter
	committed bool
	status    int
	header    http.Header
	scratch   http.Header
	body      bytes.Buffer
}

func (w *captureWriter) commit() { w.committed = true }

func (w *captureWriter) Header() http.Header {
	if !w.committed {
		if w.scratch == nil {
			w.scratch = http.Header{}
		}
		return w.scratch
	}
	return w.ResponseWriter.Header()
}

// capture records a non-committed response for the ring loop to inspect or
// replay verbatim.
func (w *captureWriter) capture(status int, header http.Header, body []byte) {
	w.status = status
	w.header = header
	w.body.Reset()
	w.body.Write(body)
}

// headerValue reads captured headers; safe on transport-error paths where no
// upstream response ever arrived.
func (w *captureWriter) headerValue(name string) string {
	if w.header == nil {
		return ""
	}
	return w.header.Get(name)
}

func (w *captureWriter) WriteHeader(code int) {
	if w.committed {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.status = code
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if w.committed {
		return w.ResponseWriter.Write(p)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *captureWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok && w.committed {
		f.Flush()
	}
}

// flushCaptured replays a captured non-committed response to the client
// verbatim — statuses that never trigger failover reach the caller unchanged.
// Content-Length is dropped: the captured body may be truncated by the
// preview limit and Go recomputes the framing.
func (w *captureWriter) flushCaptured() {
	header := w.header
	if header == nil {
		header = w.scratch
	}
	for k, vs := range header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.ResponseWriter.Header().Add(k, v)
		}
	}
	if w.status == 0 {
		w.status = http.StatusInternalServerError
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.body.Bytes())
}

func (w *captureWriter) commitJSON(status int, body string) {
	w.commit()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// Listen binds loopback and starts serving (same lifecycle contract as
// Server.Listen). The listener reserves the port before Serve so health
// checks can race safely.
func (s *PoolServer) Listen(host string, port int) error {
	if err := AssertLoopbackHost(host); err != nil {
		return err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return anyerr.Wrapf(err, "listen "+addr, "PROXY_START_FAILED")
	}
	s.endpoint = "http://" + normalizedLoopback(host) + ":" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.errCh = make(chan error, 1)
	go func() { s.errCh <- s.srv.Serve(ln) }()
	return nil
}

// Endpoint returns the bound loopback URL (set by Listen).
func (s *PoolServer) Endpoint() string { return s.endpoint }

// WaitReady polls /health until it answers or times out.
func (s *PoolServer) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(s.endpoint + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = err
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		return anyerr.Newf("pool did not become healthy: "+lastErr.Error(), "PROXY_START_FAILED")
	}
	return anyerr.Newf("pool did not become healthy", "PROXY_START_FAILED")
}

// Close stops serving. Idempotent. The listener closes before any record
// cleanup so a stop never races an in-flight bind.
func (s *PoolServer) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	s.srv = nil
	return err
}

// Err reports a serve failure (accept loop crash).
func (s *PoolServer) Err() <-chan error { return s.errCh }
