package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIsContextWindowError(t *testing.T) {
	// structured code checks
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"code":"context_length_exceeded"}`, true},
		{`{"type":"context_length_exceeded"}`, true},
		{`{"error":{"code":"context_length_exceeded"}}`, true},
		{`{"error":{"type":"context_length_exceeded"}}`, true},
		{`{"response":{"error":{"code":"context_length_exceeded"}}}`, true},
		{`{"response":{"error":{"type":"context_length_exceeded"}}}`, true},
		// raw non-JSON containing the code bypasses structured verification and returns true
		{`not json but "context_length_exceeded" inside`, true},
		// message pattern matches
		{`{"error":{"message":"prompt is too long"}}`, true},
		{`{"error":{"message":"input is too long for requested model foo"}}`, true},
		{`{"error":{"message":"exceeds the context window of this model"}}`, true},
		{`{"error":{"message":"tokens in request more than max tokens allowed"}}`, true},
		{`{"error":{"message":"[1261] something"}}`, true},
		{`{"error":{"message":"request entity too large"}}`, true},
		{`{"message":"maximum prompt length is 4096"}}`, true},
		{`{"message":"model_context_window_exceeded"}}`, true},
		// negative
		{`{"error":{"message":"rate limit exceeded"}}`, false},
		{`{"error":{"message":"hello world"}}`, false},
		{`plain text without code`, false},
		// code substring but structured check fails (no matching code field) -> message check decides
		{`{"code":"other","error":{"message":"hello"}}`, false},
	}
	for i, c := range cases {
		if got := IsContextWindowError(c.raw); got != c.want {
			t.Errorf("case %d: IsContextWindowError(%q) = %v want %v", i, c.raw, got, c.want)
		}
	}
	// checkCode direct
	if !checkCode(map[string]any{"code": ContextWindowErrorCode}) {
		t.Error("checkCode code")
	}
	if !checkCode(map[string]any{"type": ContextWindowErrorCode}) {
		t.Error("checkCode type")
	}
	if checkCode(map[string]any{"code": "other"}) {
		t.Error("checkCode false positive")
	}
}

func TestExtractMessage(t *testing.T) {
	if got := extractMessage(`{"error":{"message":"hello"}}`); got != "hello" {
		t.Errorf("extract error.message = %q", got)
	}
	if got := extractMessage(`{"response":{"error":{"message":"resp-hello"}}}`); got != "resp-hello" {
		t.Errorf("extract response.error.message = %q", got)
	}
	if got := extractMessage(`{"message":"top"}`); got != "top" {
		t.Errorf("extract message = %q", got)
	}
	if got := extractMessage(`not json`); got != "not json" {
		t.Errorf("extract raw = %q", got)
	}
	// invalid JSON object but valid JSON array -> falls through to raw
	if got := extractMessage(`[]`); got != `[]` {
		t.Errorf("extract array = %q", got)
	}
}

func TestAnthropicContextWindowMessage(t *testing.T) {
	// regex branch
	raw := `{"error":{"message":"prompt is too long: 9000 tokens > 4096 maximum"}}`
	if got := anthropicContextWindowMessage(raw); got != "prompt is too long: 9000 tokens > 4096 maximum" {
		t.Errorf("anthropic regex = %q", got)
	}
	// short message branch
	raw2 := `{"error":{"message":"something context window exceeded"}}`
	got2 := anthropicContextWindowMessage(raw2)
	if !strings.Contains(got2, "prompt is too long") || !strings.Contains(got2, "something") {
		t.Errorf("anthropic short = %q", got2)
	}
	// generic fallback when message is empty or too long (200+ chars triggers /compact path)
	if got := anthropicContextWindowMessage(`{"error":{"message":""}}`); !strings.Contains(got, "/compact") {
		t.Errorf("anthropic empty = %q", got)
	}
	// long message (>800) also generic
	long := `{"error":{"message":"` + strings.Repeat("x", 900) + `"}}`
	if got := anthropicContextWindowMessage(long); !strings.Contains(got, "/compact") {
		t.Errorf("anthropic long = %q", got)
	}
}

func TestNewAnthropicRequestID(t *testing.T) {
	id := newAnthropicRequestID()
	if !strings.HasPrefix(id, "req_") || len(id) != 28 {
		t.Errorf("request id = %q", id)
	}
}

func TestContextWindowErrorBodies(t *testing.T) {
	// anthropic with provided id
	body := anthropicContextWindowErrorBody(`{"error":{"message":"prompt is too long"}}`, "req_custom")
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		t.Fatalf("anthropic body json %s", body)
	}
	if m["request_id"] != "req_custom" {
		t.Errorf("request_id = %v", m["request_id"])
	}
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != ContextWindowErrorCode {
		t.Errorf("code = %v", errObj["code"])
	}
	// anthropic with empty id but raw contains request_id
	body2 := anthropicContextWindowErrorBody(`{"request_id":"upstream-123","error":{"message":"hi"}}`, "")
	var m2 map[string]any
	_ = json.Unmarshal(body2, &m2)
	if m2["request_id"] != "upstream-123" {
		t.Errorf("upstream request_id = %v", m2["request_id"])
	}
	// anthropic with empty id and no upstream -> generated
	body3 := anthropicContextWindowErrorBody(`{}`, "   ")
	var m3 map[string]any
	_ = json.Unmarshal(body3, &m3)
	if rid, _ := m3["request_id"].(string); !strings.HasPrefix(rid, "req_") {
		t.Errorf("generated request_id = %v", m3["request_id"])
	}
	// openai body shape
	body4 := openAIContextWindowErrorBody()
	var m4 map[string]any
	_ = json.Unmarshal(body4, &m4)
	err4, _ := m4["error"].(map[string]any)
	if err4["code"] != ContextWindowErrorCode || err4["message"] != openAIContextWindowMessage {
		t.Errorf("openai body = %s", body4)
	}
}

func TestProxyServerHelpers(t *testing.T) {
	// normalizedLoopback
	if got := normalizedLoopback("localhost"); got != "127.0.0.1" {
		t.Errorf("localhost -> %q", got)
	}
	if got := normalizedLoopback("[::1]"); got != "127.0.0.1" {
		t.Errorf("[::1] -> %q", got)
	}
	if got := normalizedLoopback("127.0.0.1"); got != "127.0.0.1" {
		t.Errorf("127.0.0.1 -> %q", got)
	}
	// writeJSONError writes correct status and type
	rec := httptest.NewRecorder()
	writeJSONError(rec, 429, "rate_limited", "too many")
	if rec.Code != 429 {
		t.Errorf("status = %d", rec.Code)
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if errObj, _ := payload["error"].(map[string]any); errObj["type"] != "rate_limited" {
		t.Errorf("payload = %s", rec.Body.String())
	}
	// httpClient lazy init
	s := &Server{}
	if s.httpClient() == nil {
		t.Error("httpClient nil")
	}
	// injectUpstreamAuth default scheme
	u, _ := url.Parse("https://api.example/v1")
	s2 := &Server{Upstream: u, UpstreamAuth: "sk-test"}
	req, _ := http.NewRequest("GET", "http://127.0.0.1/v1/models", nil)
	req.Header.Set("Authorization", "Bearer proxy-token")
	s2.injectUpstreamAuth(req)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("inject default = %q", got)
	}
	// custom header
	s3 := &Server{Upstream: u, UpstreamAuth: "k", UpstreamAuthHeader: "x-api-key", UpstreamAuthValuePrefix: ""}
	req2, _ := http.NewRequest("GET", "http://127.0.0.1/v1/models", nil)
	s3.injectUpstreamAuth(req2)
	if got := req2.Header.Get("x-api-key"); got != "k" {
		t.Errorf("inject custom = %q", got)
	}
	// Serve with nil upstream returns 502
	rec2 := httptest.NewRecorder()
	req3, _ := http.NewRequest("GET", "http://127.0.0.1/v1/models", nil)
	Serve(rec2, req3, nil)
	if rec2.Code != http.StatusBadGateway {
		t.Errorf("Serve nil upstream = %d", rec2.Code)
	}
	upNilURL := &Upstream{BaseURL: nil, Auth: "x"}
	rec3 := httptest.NewRecorder()
	Serve(rec3, req3, upNilURL)
	if rec3.Code != http.StatusBadGateway {
		t.Errorf("Serve nil baseURL = %d", rec3.Code)
	}
}

func TestProxyServiceHelpers(t *testing.T) {
	// portOf
	if got := portOf("http://127.0.0.1:4242"); got != 4242 {
		t.Errorf("portOf = %d", got)
	}
	if got := portOf("not-a-url"); got != 0 {
		t.Errorf("portOf bad = %d", got)
	}
	// processAlive for current pid should be true, for impossible pid false
	if !processAlive(ownPID()) {
		t.Error("own pid should be alive")
	}
	if processAlive(999999) {
		// extremely unlikely to be alive; if it is, skip
		t.Logf("pid 999999 unexpectedly alive")
	}
}

// ---- context-window rewrite through Serve (the [1261] regression) ----

const overflowBody = `{"error":{"message":"Upstream request failed: [1261] Prompt exceeds max length"}}`

func newOverflowUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(overflowBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func startContextTestServer(t *testing.T, s *Server) *Server {
	t.Helper()
	if err := s.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Plain OpenAI passthrough must surface the canonical openai envelope so
// Codex-class clients classify the failure as a context overflow.
func TestServeRewritesContextOverflowOnPassthrough(t *testing.T) {
	up := newOverflowUpstream(t)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := startContextTestServer(t, &Server{
		ProviderID: "opencode-go", AccountName: "work",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream: u, UpstreamAuth: "sk-go",
	})

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", resp.StatusCode, raw)
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatalf("body = %s", raw)
	}
	if payload.Error.Code != ContextWindowErrorCode || payload.Error.Type != "invalid_request_error" {
		t.Errorf("envelope = %s", raw)
	}
	if !strings.Contains(payload.Error.Message, "context window") {
		t.Errorf("message = %q", payload.Error.Message)
	}
}

// Native Anthropic targets get the anthropic error envelope plus request-id,
// which is what Claude Code's reactive compactor keys off.
func TestServeRewritesContextOverflowOnNativeAnthropic(t *testing.T) {
	up := newOverflowUpstream(t)
	aURL, err := url.Parse(up.URL + "/anthropic")
	if err != nil {
		t.Fatal(err)
	}
	s := startContextTestServer(t, &Server{
		ProviderID: "zai", AccountName: "plan",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream:     mustURLT(t, up.URL+"/openai"),
		UpstreamAuth: "sk-openai",
		Anthropic:    &UpstreamTarget{BaseURL: aURL, AuthHeader: "x-api-key"},
	})

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/messages",
		strings.NewReader(`{"model":"glm-4.7","max_tokens":16,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", resp.StatusCode, raw)
	}
	var payload struct {
		Type string `json:"type"`
		Err  struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatalf("body = %s", raw)
	}
	if payload.Type != "error" || payload.Err.Code != ContextWindowErrorCode ||
		payload.Err.Type != "invalid_request_error" {
		t.Errorf("envelope = %s", raw)
	}
	if got := resp.Header.Get("request-id"); !strings.HasPrefix(got, "req_") {
		t.Errorf("request-id header = %q", got)
	}
	if !strings.Contains(payload.Err.Message, "[1261]") {
		t.Errorf("human hint lost: %q", payload.Err.Message)
	}
}

// The translate path (OpenCode Go has no native /messages for some profiles)
// must not io.Copy the raw upstream 400 into an Anthropic client.
func TestServeRewritesContextOverflowOnTranslatedMessages(t *testing.T) {
	up := newOverflowUpstream(t)
	s := startContextTestServer(t, &Server{
		ProviderID: "deepseek", AccountName: "w",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream:           mustURLT(t, up.URL),
		UpstreamAuth:       "sk-ds",
		TranslateAnthropic: true,
	})

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), ContextWindowErrorCode) {
		t.Errorf("translated path leaked raw upstream body: %s", raw)
	}
}

// Non-context upstream failures must pass through untouched.
func TestServeLeavesNonContextErrorsAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer srv.Close()
	s := startContextTestServer(t, &Server{
		ProviderID: "p", AccountName: "a",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream: mustURLT(t, srv.URL), UpstreamAuth: "k",
	})

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if strings.Contains(string(raw), ContextWindowErrorCode) {
		t.Errorf("non-context error was rewritten: %s", raw)
	}
}

// Streaming Responses requests receive the response.failed SSE event that
// Codex's parser maps to its compaction trigger.
func TestServeRewritesContextOverflowToResponsesSSE(t *testing.T) {
	up := newOverflowUpstream(t)
	s := startContextTestServer(t, &Server{
		ProviderID: "p", AccountName: "a",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream: mustURLT(t, up.URL), UpstreamAuth: "k",
	})

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/responses",
		strings.NewReader(`{"model":"m","input":"x","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q", ct)
	}
	body := string(raw)
	if !strings.Contains(body, "event: response.failed") ||
		!strings.Contains(body, ContextWindowErrorCode) {
		t.Errorf("sse body = %s", body)
	}
}
