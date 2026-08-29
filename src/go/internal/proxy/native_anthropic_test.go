package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Native Anthropic forwarding: a vendor /anthropic base receives the request
// path verbatim with its own auth presentation — never translated.
func TestServerNativeAnthropicPassthrough(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("x-api-key")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL + "/anthropic")
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		ProviderID: "zai", AccountName: "plan",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream:     mustURLT(t, "https://api.z.ai/api/coding/paas/v4"),
		UpstreamAuth: "sk-openai",
		Anthropic: &UpstreamTarget{
			BaseURL: u,
			// Vendor expects x-api-key on the anthropic surface.
			AuthHeader: "x-api-key",
		},
	}
	if err := s.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WaitReady(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/messages",
		strings.NewReader(`{"model":"glm-4.7","max_tokens":16,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if gotPath != "/anthropic/v1/messages" {
		t.Errorf("upstream path = %q, want /anthropic/v1/messages", gotPath)
	}
	if gotAuth != s.UpstreamAuth {
		t.Errorf("auth = %q, want snapshot credential", gotAuth)
	}
	if !strings.Contains(gotBody, `"max_tokens":16`) {
		t.Errorf("body was rewritten: %s", gotBody)
	}
}

// Translation fallback still applies when there is no native target.
func TestServerTranslatedMessagesWhenNoNativeTarget(t *testing.T) {
	var gotPath string
	var gotAuthHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"c","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()
	u := mustURLT(t, upstream.URL)

	s := &Server{
		ProviderID: "deepseek", AccountName: "w",
		InstanceID: MustNewInstanceID(), Token: MustNewToken(),
		Upstream: u, UpstreamAuth: "sk-ds",
		TranslateAnthropic: true,
	}
	if err := s.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	req, _ := http.NewRequest("POST", s.Endpoint+"/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"type":"message"`) {
		t.Fatalf("translated response: %d %s", resp.StatusCode, body)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotAuthHeader != "Bearer sk-ds" {
		t.Errorf("auth = %q", gotAuthHeader)
	}
}

func mustURLT(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
