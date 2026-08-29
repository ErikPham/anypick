package proxy

// Benchmarks backing scripts/perf-baseline.mjs and the Milestone 0 baseline
// contract of the native cutover (ADR 0020). They isolate the proxy's
// ADDITIVE cost: every upstream here is an in-process httptest stub that
// answers immediately, so proxied-vs-direct differences measure only local
// translation, authentication, and framing work — never network latency.
//
// Run with:
//
//	go test -bench BenchmarkBaseline -run '^$' -benchmem ./internal/proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const benchChatBody = `{"model":"gpt-bench","messages":[{"role":"user","content":"hi"}]}`

// ttfbTotal accumulates first-byte latencies across iterations; benchmarks
// report the mean once after the loop (ReportMetric overwrites same-unit
// metrics, so per-iteration reporting would keep only the last sample).
var ttfbTotal float64

func benchClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

func benchPost(b *testing.B, url, token string) {
	b.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(benchChatBody))
	if err != nil {
		b.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := benchClient().Do(req)
	if err != nil {
		b.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("status = %d", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		b.Fatal(err)
	}
	resp.Body.Close()
}

// BenchmarkBaselineDirectRequest is the zero-proxy reference point: one HTTP
// hop to a loopback stub. Subtract its ns/op from BenchmarkBaselineProxyRequest
// to get the proxy's additive non-streaming overhead.
func BenchmarkBaselineDirectRequest(b *testing.B) {
	up := newUpstreamProbe(b)
	upURL := up.url.String()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchPost(b, upURL, "irrelevant-upstream-token")
	}
}

// BenchmarkBaselineProxyRequest sends the identical request through the
// authenticated translation proxy instead of straight to the stub.
func BenchmarkBaselineProxyRequest(b *testing.B) {
	up := newUpstreamProbe(b)
	s := newTestServer(b, up.url)
	endpoint := s.Endpoint
	token := s.Token
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchPost(b, endpoint, token)
	}
}

// streamingUpstream flushes SSE headers and a first event immediately, then a
// terminator. firstByte records when the first body byte left the handler so
// the benchmark can separate queueing from proxy processing.
func streamingUpstream(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			b.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			b.Error("no flusher")
			return
		}
		_, _ = io.WriteString(w, "data: {\"delta\":\"a\"}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	b.Cleanup(srv.Close)
	return srv
}

func benchFirstByte(b *testing.B, endpoint, token string) {
	b.Helper()
	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/chat/completions", strings.NewReader(benchChatBody))
	if err != nil {
		b.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := benchClient().Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		b.Fatal(err)
	}
	firstByte := time.Since(start).Seconds() * 1000
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		b.Fatal(err)
	}
	// Accumulate and report once: ReportMetric overwrites same-unit metrics,
	// so per-iteration calls would publish only the final sample.
	ttfbTotal += firstByte
}

// BenchmarkBaselineDirectFirstByte measures mean time-to-first-byte against
// the stub directly; BenchmarkBaselineProxyFirstByte adds the proxy hop. The
// difference of the two means is the additive streaming first-byte overhead
// budget input (Milestone 4).
func BenchmarkBaselineDirectFirstByte(b *testing.B) {
	srv := streamingUpstream(b)
	u, err := url.Parse(srv.URL)
	if err != nil {
		b.Fatal(err)
	}
	direct := u.String()
	ttfbTotal = 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchFirstByte(b, direct, "irrelevant-upstream-token")
	}
	b.ReportMetric(ttfbTotal/float64(b.N), "ttfb-ms")
}

func BenchmarkBaselineProxyFirstByte(b *testing.B) {
	srv := streamingUpstream(b)
	u, err := url.Parse(srv.URL)
	if err != nil {
		b.Fatal(err)
	}
	s := newTestServer(b, u)
	endpoint := s.Endpoint
	token := s.Token
	ttfbTotal = 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchFirstByte(b, endpoint, token)
	}
	b.ReportMetric(ttfbTotal/float64(b.N), "ttfb-ms")
}
