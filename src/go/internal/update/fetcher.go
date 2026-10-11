package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAPIBase = "https://api.github.com/repos/outof0/anypick"
	userAgent      = "AnyPick-Update/1.0"
)

// ErrNotModified is returned (with notModified=true) when the server answers 304.
var ErrNotModified = errors.New("not modified")

// ErrRateLimited is returned for HTTP 403/429 from the GitHub API.
var ErrRateLimited = errors.New("rate limited")

// ErrOffline wraps a transport-level failure (DNS, dial, timeout).
type ErrOffline struct{ Err error }

func (e *ErrOffline) Error() string {
	if e.Err == nil {
		return "offline"
	}
	return "offline: " + e.Err.Error()
}
func (e *ErrOffline) Unwrap() error { return e.Err }

// Fetcher loads the latest published release.
type Fetcher interface {
	Latest(ctx context.Context, etag string) (rel Release, newETag string, notModified bool, err error)
}

// GitHubFetcher talks to the GitHub Releases API.
type GitHubFetcher struct {
	HTTP    *http.Client
	BaseURL string // API base, e.g. https://api.github.com/repos/outof0/anypick
	Env     Env
}

func (f *GitHubFetcher) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return http.DefaultClient
}

func (f *GitHubFetcher) base() string {
	if override := HookFeedURL(f.Env); override != "" {
		return override
	}
	if f.BaseURL != "" {
		return strings.TrimRight(f.BaseURL, "/")
	}
	return defaultAPIBase
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Latest fetches /releases/latest. Drafts and prereleases are rejected even if
// the API somehow returns them. 304 sets notModified. 403/429 return
// ErrRateLimited without panicking. Transport errors return *ErrOffline.
func (f *GitHubFetcher) Latest(ctx context.Context, etag string) (Release, string, bool, error) {
	url := f.base() + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return Release{}, "", false, &ErrOffline{Err: err}
	}
	defer resp.Body.Close()
	newETag := resp.Header.Get("ETag")
	switch resp.StatusCode {
	case http.StatusNotModified:
		return Release{}, etagOr(newETag, etag), true, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Release{}, etagOr(newETag, etag), false, ErrRateLimited
	case http.StatusOK:
		// continue
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Release{}, "", false, fmt.Errorf("github releases: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Release{}, "", false, fmt.Errorf("decode release: %w", err)
	}
	if raw.Draft || raw.Prerelease {
		return Release{}, "", false, fmt.Errorf("latest release %q is draft/prerelease; ignored", raw.TagName)
	}
	ver, err := ParseVersion(raw.TagName)
	if err != nil {
		return Release{}, "", false, fmt.Errorf("release tag %q: %w", raw.TagName, err)
	}
	if ver.IsPrerelease() {
		return Release{}, "", false, fmt.Errorf("latest release %q has prerelease tag; ignored", raw.TagName)
	}
	assets := make([]Asset, 0, len(raw.Assets))
	for _, a := range raw.Assets {
		if err := ValidateAssetName(a.Name); err != nil {
			return Release{}, "", false, fmt.Errorf("release asset: %w", err)
		}
		assets = append(assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	rel := Release{
		Tag:         raw.TagName,
		Version:     ver.String(),
		Notes:       raw.Body,
		PublishedAt: raw.PublishedAt,
		Prerelease:  raw.Prerelease,
		Draft:       raw.Draft,
		Assets:      assets,
		HTMLURL:     raw.HTMLURL,
	}
	return rel, etagOr(newETag, etag), false, nil
}

func etagOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// AttachChecksums fetches SHA256SUMS.txt from the release assets (by name) and
// fills Asset.SHA256. Fail-closed: missing SUMS file, non-200 / offline fetch,
// unparseable body, duplicate entries, or any installable asset without a
// non-empty hash returns an error. The caller must not proceed to install.
func AttachChecksums(ctx context.Context, client *http.Client, rel Release) (Release, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var sumsURL string
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS.txt" {
			sumsURL = a.URL
			break
		}
	}
	if sumsURL == "" {
		return rel, fmt.Errorf("SHA256SUMS.txt missing from release assets")
	}
	// Callers that download installable assets must also ValidateDownloadURL.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return rel, &ErrOffline{Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rel, fmt.Errorf("SHA256SUMS.txt: HTTP %d", resp.StatusCode)
	}
	sums, err := ParseSHA256SUMS(resp.Body)
	if err != nil {
		return rel, err
	}
	rel.Assets = ApplyChecksums(rel.Assets, sums)
	if err := RequireChecksums(rel.Assets); err != nil {
		return rel, err
	}
	return rel, nil
}
