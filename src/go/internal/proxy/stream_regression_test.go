package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The access-log StatusRecorder must preserve http.Flusher: without it the
// streaming translator bails before writing a byte and every streamed
// translation comes back `200` with an empty body (regression that broke
// all streaming clients, e.g. Claude Code).
func TestStatusRecorderImplementsFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := NewStatusRecorder(rec)
	if _, ok := interface{}(sr).(http.Flusher); !ok {
		t.Fatal("StatusRecorder must implement http.Flusher")
	}
}

// Full Handler stack with a fake SSE upstream: translated Anthropic stream
// must carry content end to end through the status recorder.
func TestHandlerStreamsTranslatedSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"1\",\"model\":\"m\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	srv := &Server{ProviderID: "p", AccountName: "a", Token: "tok", Upstream: base,
		UpstreamAuth: "x", TranslateAnthropic: true}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d ct=%q body=%q", res.StatusCode, res.Header.Get("Content-Type"), b)
	}
	if !strings.Contains(string(b), "OK") {
		t.Fatalf("translated stream lost content: %q", b)
	}
}
