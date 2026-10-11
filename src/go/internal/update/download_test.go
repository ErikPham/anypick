package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func testTransport(t *testing.T, srv *httptest.Server) *Transport {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &Transport{
		AllowedHosts: []string{u.Hostname()},
		HTTP:         srv.Client(),
	}
}

func TestValidateDownloadURLAllowlist(t *testing.T) {
	ok := []string{
		"https://github.com/outof0/anypick/releases/download/v1.1.0/x.zip",
		"https://objects.githubusercontent.com/github-production-release-asset/123/x",
		"https://release-assets.githubusercontent.com/github-production-release-asset/123/x",
	}
	for _, u := range ok {
		if err := ValidateDownloadURL(u); err != nil {
			t.Fatalf("allow %q: %v", u, err)
		}
	}
	bad := []string{
		"http://github.com/outof0/anypick/x",
		"https://evil.com/x",
		"https://api.github.com/repos/outof0/anypick/x",
		"https://github.com.evil.com/x",
		"ftp://github.com/x",
		"",
	}
	for _, u := range bad {
		if err := ValidateDownloadURL(u); err == nil {
			t.Fatalf("expected reject for %q", u)
		}
	}
}

func TestRedirectToReleaseAssetsAllowed(t *testing.T) {
	tr := &Transport{} // production allowlist
	req, err := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/foo", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := []*http.Request{mustReq(t, "https://github.com/outof0/anypick/releases/download/v1.1.0/x")}
	if err := tr.checkRedirect(req, via); err != nil {
		t.Fatalf("release-assets redirect must be allowed: %v", err)
	}
}

func TestRedirectToOtherHostRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/stolen", http.StatusFound)
	}))
	defer srv.Close()

	tr := testTransport(t, srv)
	_, err := tr.Get(context.Background(), srv.URL+"/start")
	if err == nil {
		t.Fatal("redirect to other host must be refused")
	}
	if !strings.Contains(err.Error(), "not allowed") && !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("want allowlist refusal, got %v", err)
	}
}

func TestHTTPSToHTTPDowngradeRefused(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("http hop must not be reached")
	}))
	defer httpSrv.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpSrv.URL+"/downgrade", http.StatusFound)
	}))
	defer tlsSrv.Close()

	tr := testTransport(t, tlsSrv)
	_, err := tr.Get(context.Background(), tlsSrv.URL+"/start")
	if err == nil {
		t.Fatal("https→http downgrade must be refused")
	}
	if !strings.Contains(err.Error(), "https only") && !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("want scheme refusal, got %v", err)
	}
}

func TestTooManyRedirectsRefused(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/next", http.StatusFound)
	}))
	defer srv.Close()

	tr := testTransport(t, srv)
	_, err := tr.Get(context.Background(), srv.URL+"/start")
	if err == nil {
		t.Fatal("too many redirects must be refused")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("want redirect cap error, got %v", err)
	}
}

func TestRedirectSameAllowlistedHostOK(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tr := testTransport(t, srv)
	resp, err := tr.Get(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
}

func TestDefaultHTTPClientNoDefaultClient(t *testing.T) {
	c := DefaultHTTPClient()
	if c == nil || c.CheckRedirect == nil {
		t.Fatal("DefaultHTTPClient must install CheckRedirect")
	}
	if c == http.DefaultClient {
		t.Fatal("must not return http.DefaultClient")
	}
}

func TestTransportInstallsCheckRedirectOnInjectedClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	tr := testTransport(t, srv)
	c := tr.Client()
	if c.CheckRedirect == nil {
		t.Fatal("CheckRedirect must be installed on injected client")
	}
	resp, err := tr.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestLiveGitHubSHA256SUMS(t *testing.T) {
	if os.Getenv("ANYPICK_UPDATE_LIVE_TEST") != "1" {
		t.Skip("set ANYPICK_UPDATE_LIVE_TEST=1 to enable live GitHub download")
	}
	const rawURL = "https://github.com/outof0/anypick/releases/download/v1.1.0/SHA256SUMS.txt"
	tr := &Transport{} // production allowlist + CheckRedirect
	resp, err := tr.Get(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("live download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	sums, err := ParseSHA256SUMS(resp.Body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(sums) == 0 {
		t.Fatal("expected checksum entries from real v1.1.0 SHA256SUMS.txt")
	}
	if _, ok := sums["AnyPick-macos-universal.zip"]; !ok {
		t.Fatalf("missing expected asset in sums: keys=%d", len(sums))
	}
}

func mustReq(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
