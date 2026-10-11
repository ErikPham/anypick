package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcherETag304(t *testing.T) {
	var hits int
	var sawIfNone string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		sawIfNone = r.Header.Get("If-None-Match")
		if sawIfNone == `"abc"` {
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
	if sawIfNone != `"abc"` {
		t.Fatalf("If-None-Match not re-sent: %q", sawIfNone)
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

func TestFetcherUnparseableTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "not-a-version", "draft": false, "prerelease": false, "assets": []any{},
		})
	}))
	defer srv.Close()
	f := &GitHubFetcher{HTTP: srv.Client(), BaseURL: srv.URL}
	_, _, _, err := f.Latest(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for unparseable tag")
	}
}

func TestFetcherRejectsPathLikeAssetName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.2.0", "draft": false, "prerelease": false,
			"assets": []map[string]any{{"name": "../evil.zip", "browser_download_url": "https://github.com/x", "size": 1}},
		})
	}))
	defer srv.Close()
	f := &GitHubFetcher{HTTP: srv.Client(), BaseURL: srv.URL}
	_, _, _, err := f.Latest(context.Background(), "")
	if err == nil {
		t.Fatal("expected reject for path-like asset name")
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

func TestFetcherFeedURLHookClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not hit override server when gate closed")
	}))
	defer srv.Close()
	// Gate closed (TESTING unset) — FEED_URL ignored; BaseURL used.
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.1.0", "draft": false, "prerelease": false, "assets": []any{},
		})
	}))
	defer real.Close()
	env := MapEnv{EnvFeedURL: srv.URL} // no TESTING=1
	f := &GitHubFetcher{HTTP: real.Client(), BaseURL: real.URL, Env: env}
	rel, _, _, err := f.Latest(context.Background(), "")
	if err != nil || rel.Version != "1.1.0" {
		t.Fatalf("rel=%+v err=%v", rel, err)
	}
}

func TestAttachChecksums(t *testing.T) {
	goodBody := strings.Repeat("ab", 32) + "  file.bin\n"
	t.Run("200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(goodBody))
		}))
		defer srv.Close()
		rel := Release{Assets: []Asset{
			{Name: "file.bin", URL: "https://github.com/x/file.bin"},
			{Name: "SHA256SUMS.txt", URL: srv.URL},
		}}
		out, err := AttachChecksums(context.Background(), srv.Client(), rel)
		if err != nil {
			t.Fatal(err)
		}
		if out.Assets[0].SHA256 != strings.Repeat("ab", 32) {
			t.Fatalf("hash %q", out.Assets[0].SHA256)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		rel := Release{Assets: []Asset{{Name: "file.bin"}}}
		if _, err := AttachChecksums(context.Background(), nil, rel); err == nil {
			t.Fatal("missing SHA256SUMS.txt must fail closed")
		}
	})
	t.Run("404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
		}))
		defer srv.Close()
		rel := Release{Assets: []Asset{
			{Name: "file.bin"},
			{Name: "SHA256SUMS.txt", URL: srv.URL},
		}}
		if _, err := AttachChecksums(context.Background(), srv.Client(), rel); err == nil {
			t.Fatal("404 must fail")
		}
	})
	t.Run("500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer srv.Close()
		rel := Release{Assets: []Asset{
			{Name: "file.bin"},
			{Name: "SHA256SUMS.txt", URL: srv.URL},
		}}
		if _, err := AttachChecksums(context.Background(), srv.Client(), rel); err == nil {
			t.Fatal("500 must fail")
		}
	})
	t.Run("unparseable body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not-a-sums-file\n"))
		}))
		defer srv.Close()
		rel := Release{Assets: []Asset{
			{Name: "file.bin"},
			{Name: "SHA256SUMS.txt", URL: srv.URL},
		}}
		if _, err := AttachChecksums(context.Background(), srv.Client(), rel); err == nil {
			t.Fatal("unparseable must fail")
		}
	})
	t.Run("offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		u := srv.URL
		srv.Close()
		rel := Release{Assets: []Asset{
			{Name: "file.bin"},
			{Name: "SHA256SUMS.txt", URL: u},
		}}
		_, err := AttachChecksums(context.Background(), http.DefaultClient, rel)
		var off *ErrOffline
		if !errors.As(err, &off) {
			t.Fatalf("want ErrOffline, got %v", err)
		}
	})
	t.Run("missing entry", func(t *testing.T) {
		body := strings.Repeat("cd", 32) + "  other.bin\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		rel := Release{Assets: []Asset{
			{Name: "file.bin"},
			{Name: "SHA256SUMS.txt", URL: srv.URL},
		}}
		if _, err := AttachChecksums(context.Background(), srv.Client(), rel); err == nil {
			t.Fatal("missing entry for file.bin must fail closed")
		}
	})
}
