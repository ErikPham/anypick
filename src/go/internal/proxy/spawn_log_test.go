package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The runtime dir may not exist for accounts saved after the TS era; the
// spawner must create it instead of running the child with nil outputs.
func TestOpenProxyLogFileCreatesRuntimeDir(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "providers", "workbuddy", "accounts", "work", "runtime", "proxy.log")
	f, err := openProxyLogFile(logPath)
	if err != nil {
		t.Fatalf("openProxyLogFile: %v", err)
	}
	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	raw, err := os.ReadFile(logPath)
	if err != nil || string(raw) != "hello\n" {
		t.Fatalf("log content = %q, %v", raw, err)
	}
}

// Every proxied request leaves one secret-free, design-shaped access line.
// The leak check is the point: proxy.log gets attached to bug reports, so a
// credential in it is a credential published.
func TestHandlerWritesAccessLog(t *testing.T) {
	var buf bytes.Buffer
	old := proxyLogSink
	proxyLogSink = &buf
	defer func() { proxyLogSink = old }()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL)
	srv := &Server{
		ProviderID: "workbuddy", AccountName: "main", Token: "proxy-secret",
		Upstream: base, UpstreamAuth: "sub-token",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer proxy-secret")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	line := buf.String()
	for _, want := range []string{"POST", "/v1/chat/completions", "200"} {
		if !strings.Contains(line, want) {
			t.Fatalf("access log = %q, want it to contain %q", line, want)
		}
	}
	for _, leak := range []string{"proxy-secret", "sub-token"} {
		if strings.Contains(line, leak) {
			t.Fatalf("access log leaks a credential: %q", line)
		}
	}
}
