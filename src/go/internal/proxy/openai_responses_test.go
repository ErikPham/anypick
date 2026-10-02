package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestCollectOpenAIChatStreamKeepsParallelToolCalls replays the standard
// interleaved SSE shape for two parallel tool calls. Keying state by position
// within each chunk merged the deltas into one corrupted call — the worst kind
// of failure for a coding agent.
func TestCollectOpenAIChatStreamKeepsParallelToolCalls(t *testing.T) {
	const sse = `data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"alpha","arguments":"{\"x\":"}}]}}]}
data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"beta","arguments":"{\"y\":"}}]}}]}
data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}
data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`
	resp, err := collectOpenAIChatStream(strings.NewReader(sse), "m")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %+v", resp.Choices)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2 (%+v)", len(calls), calls)
	}
	if calls[0].ID != "call_a" || calls[0].Function.Name != "alpha" || calls[0].Function.Arguments != `{"x":1}` {
		t.Fatalf("call 0 = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Function.Name != "beta" || calls[1].Function.Arguments != `{"y":2}` {
		t.Fatalf("call 1 = %+v", calls[1])
	}
}

// TestServeOpenAIResponsesDropsClientAcceptEncoding: forwarding the caller's
// Accept-Encoding made Go skip transparent decompression, so a gzip-capable
// upstream handed compressed bytes to the JSON/SSE parser and every request
// failed as "invalid upstream stream".
func TestServeOpenAIResponsesDropsClientAcceptEncoding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enc := r.Header.Get("Accept-Encoding"); enc != "" && enc != "gzip" {
			t.Errorf("client Accept-Encoding %q was forwarded upstream", enc)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL)
	up := &Upstream{BaseURL: base, Auth: "sub-token"}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"m","input":"hello"}`))
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	serveOpenAIResponsesWith(rec, req, up)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("translated body = %s", rec.Body.String())
	}
}

// TestOpenAIResponsesErrorPathDropsFramingHeaders: the body is re-read and
// capped at 1 MiB, so the upstream's Content-Length/Content-Encoding no longer
// describe what the client receives.
func TestOpenAIResponsesErrorPathDropsFramingHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := strings.Repeat("x", 1<<20+64)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "identity")
		w.Header().Set("Content-Length", "1048640")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL)
	up := &Upstream{BaseURL: base, Auth: "sub-token"}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"m","input":"hello"}`))
	rec := httptest.NewRecorder()
	serveOpenAIResponsesWith(rec, req, up)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "" && cl != "1048576" {
		t.Fatalf("stale Content-Length forwarded: %q", cl)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Fatalf("stale Content-Encoding forwarded: %q", ce)
	}
}
