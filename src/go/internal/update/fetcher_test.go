package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetcherETag304(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v1.2.0",
			"body":         "notes",
			"draft":        false,
			"prerelease":   false,
			"html_url":     "https://example/rel",
			"published_at": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			"assets":       []map[string]any{},
		})
	}))
	defer srv.Close()

	f := &GitHubFetcher{HTTP: srv.Client(), BaseURL: srv.URL, Env: MapEnv{}}
	rel, etag, mod, err := f.Latest(context.Background(), "")
	if err != nil || mod || etag != `"abc"` || rel.Version != "1.2.0" {
		t.Fatalf("first: rel=%+v etag=%q mod=%v err=%v", rel, etag, mod, err)
	}
	_, etag2, mod2, err := f.Latest(context.Background(), `"abc"`)
	if err != nil || !mod2 || etag2 != `"abc"` {
		t.Fatalf("second: etag=%q mod=%v err=%v", etag2, mod2, err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d", hits)
	}
}

func TestFetcherRateLimited(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()
			f := &GitHubFetcher{HTTP: srv.Client(), BaseURL: srv.URL}
			_, _, _, err := f.Latest(context.Background(), "")
			if !errors.Is(err, ErrRateLimited) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestFetcherOffline(t *testing.T) {
	// Closed server → connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	f := &GitHubFetcher{HTTP: http.DefaultClient, BaseURL: url}
	_, _, _, err := f.Latest(context.Background(), "")
	var off *ErrOffline
	if !errors.As(err, &off) {
		t.Fatalf("want ErrOffline, got %v", err)
	}
}

func TestFetcherIgnoresPrerelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   "v1.2.0-rc.1",
			"prerelease": true,
			"draft":      false,
			"assets":     []any{},
		})
	}))
	defer srv.Close()
	f := &GitHubFetcher{HTTP: srv.Client(), BaseURL: srv.URL}
	_, _, _, err := f.Latest(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for prerelease")
	}
}

func TestFetcherFeedURLHook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.1.0", "draft": false, "prerelease": false, "assets": []any{},
		})
	}))
	defer srv.Close()
	env := MapEnv{EnvTesting: "1", EnvFeedURL: srv.URL}
	f := &GitHubFetcher{HTTP: srv.Client(), BaseURL: "https://example.invalid", Env: env}
	rel, _, _, err := f.Latest(context.Background(), "")
	if err != nil || rel.Version != "1.1.0" {
		t.Fatalf("rel=%+v err=%v", rel, err)
	}
}
