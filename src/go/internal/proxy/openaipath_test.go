package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/protocol/gemini"
)

// A subscription-channel upstream serves chat under a versioned base path.
// The inbound /v1/… path must never reach it: vendors read the /v1/ segment
// as an API-key channel marker (WorkBuddy answers code 11128).
func TestServeOpenAIPathRewritesChannel(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "sub-token", OpenAIPath: "/chat/completions"}

	for _, inbound := range []string{"/v1/chat/completions", "/v1/completions"} {
		gotPath, gotAuth = "", ""
		req := httptest.NewRequest(http.MethodPost, inbound, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		Serve(rec, req, up)
		if gotPath != "/v2/chat/completions" {
			t.Fatalf("POST %s reached %q, want /v2/chat/completions", inbound, gotPath)
		}
		if gotAuth != "Bearer sub-token" {
			t.Fatalf("upstream auth = %q, want subscription bearer", gotAuth)
		}
	}
}

func TestServeResponsesBridgesToOpenAIPath(t *testing.T) {
	var gotPath, gotBody, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-1","object":"chat.completion","model":"deepseek-4.1-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "sub-token", OpenAIPath: "/chat/completions"}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"deepseek-4.1-flash","input":"hello"}`))
	rec := httptest.NewRecorder()
	Serve(rec, req, up)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v2/chat/completions" {
		t.Fatalf("responses reached %q, want /v2/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sub-token" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if strings.Contains(gotBody, `"input"`) || !strings.Contains(gotBody, `"messages"`) {
		t.Fatalf("request was not converted to chat: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"stream":true`) {
		t.Fatalf("subscription request was not forced to streaming mode: %s", gotBody)
	}
	var response gemini.OpenAIResponsesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("responses body: %v (%s)", err, rec.Body.String())
	}
	if response.Object != "response" || len(response.Output) == 0 ||
		len(response.Output[0].Content) == 0 || response.Output[0].Content[0].Text != "ok" {
		t.Fatalf("translated response = %+v", response)
	}
}

func TestServeStreamingResponsesBridgesToOpenAIPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat-stream\",\"model\":\"deepseek-4.1-flash\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat-stream\",\"model\":\"deepseek-4.1-flash\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "sub-token", OpenAIPath: "/chat/completions"}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"deepseek-4.1-flash","stream":true,"input":"hello"}`))
	rec := httptest.NewRecorder()
	Serve(rec, req, up)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	stream := rec.Body.String()
	for _, event := range []string{"response.created", "response.output_text.delta", "response.completed"} {
		if !strings.Contains(stream, event) {
			t.Fatalf("stream missing %s:\n%s", event, stream)
		}
	}
}

// Claude Code speaks /v1/messages. The subscription sanitizer must run on
// the translated OpenAI body, not only on verbatim /v1/chat/completions.
func TestServeTranslatedMessagesRunsSanitizeOpenAIBody(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{
		BaseURL: base, Auth: "sub-token", TranslateAnthropic: true,
		SanitizeOpenAIBody: func(b []byte) []byte {
			return []byte(`{"model":"hy4-preview","messages":[{"role":"system","content":"shaped"}]}`)
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"hy4","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	Serve(rec, req, up)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"hy4-preview"`) || !strings.Contains(gotBody, `"shaped"`) {
		t.Fatalf("translated sanitizer did not run: %s", gotBody)
	}
}

func TestServeDefaultOpenAIPassthroughRunsSanitizeOpenAIBody(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL)
	up := &Upstream{
		BaseURL: base, Auth: "sub-token",
		SanitizeOpenAIBody: func(b []byte) []byte {
			return []byte(`{"model":"grok-compatible","messages":[]}`)
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"grok","reasoning_effort":"high"}`))
	rec := httptest.NewRecorder()
	Serve(rec, req, up)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if gotBody != `{"model":"grok-compatible","messages":[]}` {
		t.Fatalf("default passthrough sanitizer did not run: %s", gotBody)
	}
}

// Without OpenAIPath the default reverse-proxy join is preserved.
func TestServeWithoutOpenAIPathKeepsJoin(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "k"}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	Serve(httptest.NewRecorder(), req, up)
	if gotPath != "/v2/v1/chat/completions" {
		t.Fatalf("default join reached %q", gotPath)
	}
}

// TestPoolMemberResponsesUsesOpenAIPath: the pool ring must not bypass the
// OpenAIPath rewrite for /v1/responses. Without the leaf it fell through to
// the default reverse, sending the vendor's own /v1/responses path — the one
// OpenAIPath exists to avoid (WorkBuddy code 11128).
func TestPoolMemberResponsesUsesOpenAIPath(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "sub-token", OpenAIPath: "/chat/completions"}

	inner := httptest.NewRecorder()
	cw := &captureWriter{ResponseWriter: inner}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"m","input":"hello"}`))
	(&PoolServer{}).serveMember(cw, req, up)
	cw.flushCaptured()

	if gotPath != "/v2/chat/completions" {
		t.Fatalf("pool responses reached %q, want /v2/chat/completions", gotPath)
	}
	if inner.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", inner.Code, inner.Body.String())
	}
	if !strings.Contains(inner.Body.String(), `"ok"`) {
		t.Fatalf("translated body = %s", inner.Body.String())
	}
}
