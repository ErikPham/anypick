package update

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// maxRedirects caps how many redirects the shared download client will follow.
const maxRedirects = 5

// ErrDeniedDownloadURL is returned when the initial URL or a redirect hop fails
// the allowlist / https check. Callers must not treat it as ErrOffline.
type ErrDeniedDownloadURL struct{ Err error }

func (e *ErrDeniedDownloadURL) Error() string {
	if e.Err == nil {
		return "download URL denied"
	}
	return e.Err.Error()
}
func (e *ErrDeniedDownloadURL) Unwrap() error { return e.Err }

// DefaultAllowedDownloadHosts is the production allowlist for release asset
// downloads. Exact host match, https only, no wildcards.
//
// release-assets.githubusercontent.com is included because GitHub 302-redirects
// real browser_download_url responses from github.com there (verified against
// v1.1.0 SHA256SUMS.txt). Without it every real download is refused.
var DefaultAllowedDownloadHosts = []string{
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

// Transport is the shared allowlisted HTTPS client used for the GitHub release
// API fetch (redirect hops), AttachChecksums, and asset downloads.
//
// AllowedHosts defaults to DefaultAllowedDownloadHosts when nil/empty.
// Tests inject their own hosts (e.g. "127.0.0.1") and typically set HTTP to an
// *http.Client whose Transport trusts httptest.NewTLSServer.
//
// The initial URL is validated before the request; every redirect hop is
// validated by CheckRedirect (host allowlist + https only, so an https→http
// downgrade is refused). Prefer Transport over http.DefaultClient.
type Transport struct {
	AllowedHosts []string
	HTTP         *http.Client
}

func (t *Transport) hostSet() map[string]struct{} {
	hosts := DefaultAllowedDownloadHosts
	if t != nil && len(t.AllowedHosts) > 0 {
		hosts = t.AllowedHosts
	}
	out := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		out[strings.ToLower(h)] = struct{}{}
	}
	return out
}

// ValidateURL reports whether raw is an https URL on an allowlisted exact host.
func (t *Transport) ValidateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("empty download URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse download URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("download URL scheme %q not allowed (https only)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := t.hostSet()[host]; !ok {
		return fmt.Errorf("download host %q not allowed", host)
	}
	return nil
}

// ValidateDownloadURL validates raw against the production allowlist.
func ValidateDownloadURL(raw string) error {
	return (*Transport)(nil).ValidateURL(raw)
}

// checkRedirect validates each redirect target and caps hop count.
func (t *Transport) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL == nil {
		return fmt.Errorf("redirect missing URL")
	}
	return t.ValidateURL(req.URL.String())
}

// Client returns the HTTP client with per-hop redirect checks. When t.HTTP is
// set (tests), its Transport/Timeout are kept and CheckRedirect is installed
// unless the caller already set one.
func (t *Transport) Client() *http.Client {
	if t != nil && t.HTTP != nil {
		c := *t.HTTP
		if c.CheckRedirect == nil {
			c.CheckRedirect = t.checkRedirect
		}
		return &c
	}
	return &http.Client{CheckRedirect: t.checkRedirect}
}

// DefaultHTTPClient returns the production shared client (allowlist + https +
// CheckRedirect on every hop + redirect cap). Used when GitHubFetcher.HTTP is
// nil and whenever callers omit a Transport.
func DefaultHTTPClient() *http.Client {
	return (*Transport)(nil).Client()
}

// Get validates rawURL against the allowlist, then GETs it with a client that
// checks every redirect hop. Used by AttachChecksums and asset downloads.
// Allowlist failures return *ErrDeniedDownloadURL (no request is made).
func (t *Transport) Get(ctx context.Context, rawURL string) (*http.Response, error) {
	if t == nil {
		t = &Transport{}
	}
	if err := t.ValidateURL(rawURL); err != nil {
		return nil, &ErrDeniedDownloadURL{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return t.Client().Do(req)
}

// DownloadAsset GETs an installable release asset through the shared
// allowlisted client (initial URL + every redirect hop).
func DownloadAsset(ctx context.Context, tr *Transport, rawURL string) (*http.Response, error) {
	if tr == nil {
		tr = &Transport{}
	}
	return tr.Get(ctx, rawURL)
}

// ValidateAssetName rejects asset names that contain path separators or "..".
// No basename trimming — a renamed or path-like name fails closed.
func ValidateAssetName(name string) error {
	if name == "" {
		return fmt.Errorf("empty asset name")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("asset name %q contains ..", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("asset name %q contains path separator", name)
	}
	return nil
}
