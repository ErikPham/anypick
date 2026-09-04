package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
