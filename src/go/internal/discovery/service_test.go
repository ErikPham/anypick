package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/modelcache"
)

func newTestService(t *testing.T) (*Service, *modelcache.Store) {
	t.Helper()
	root := t.TempDir()
	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store := &modelcache.Store{DB: database}
	return &Service{Root: root, Cache: store}, store
}

func TestListFallsBackToCatalogWithoutLiveDiscovery(t *testing.T) {
	svc, _ := newTestService(t)
	res := svc.List(context.Background(), Request{ProviderID: "custom"})
	if res.Source != SourceCatalog || len(res.Models) == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestListLiveFetchThenCache(t *testing.T) {
	models := []string{"m-newest", "m-older"}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]string{{"id": models[0]}, {"id": models[1]}},
		})
	}))
	defer srv.Close()

	svc, store := newTestService(t)
	res := svc.List(context.Background(), Request{ProviderID: "openai", Endpoint: srv.URL, APIKey: "k"})
	if hits != 1 || res.Source != SourceLive || len(res.Models) != 2 || res.Models[0] != "m-newest" {
		t.Fatalf("live fetch wrong: hits=%d res=%+v", hits, res)
	}

	// Fresh cache answers without a network call.
	cached, _ := store.Get(context.Background(), "openai", srv.URL)
	if cached == nil {
		t.Fatal("live fetch was not cached")
	}
	res = svc.List(context.Background(), Request{ProviderID: "openai", Endpoint: srv.URL})
	if res.Source != SourceCache || hits != 1 {
		t.Fatalf("cache step wrong: hits=%d res=%+v", hits, res)
	}

	// Refresh forces the vendor.
	res = svc.List(context.Background(), Request{ProviderID: "openai", Endpoint: srv.URL, Refresh: true})
	if hits != 2 || res.Source != SourceLive {
		t.Fatalf("refresh ignored: hits=%d res=%+v", hits, res)
	}
}

func TestListVendorFailureFallsBackToStaleThenCatalog(t *testing.T) {
	up := true
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []string{"v1"}})
	}))
	defer srv.Close()
	srvURL = srv.URL

	svc, _ := newTestService(t)
	if res := svc.List(context.Background(), Request{ProviderID: "grok-api", Endpoint: srvURL}); res.Source != SourceLive {
		t.Fatalf("expected live first: %+v", res)
	}
	up = false
	res := svc.List(context.Background(), Request{ProviderID: "grok-api", Endpoint: srvURL, Refresh: true})
	if res.Source != SourceStale || res.Note == "" {
		t.Fatalf("stale fallback wrong: %+v", res)
	}
	// Unknown provider with no cache → catalog.
	res = svc.List(context.Background(), Request{ProviderID: "gemini-api"})
	if res.Source != SourceCatalog {
		t.Fatalf("catalog fallback wrong: %+v", res)
	}
}

func TestFreshnessWindowHonoursTTL(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if isFresh(old, time.Now()) {
		t.Fatal("stale timestamp treated as fresh")
	}
	recent := time.Now().UTC().Format(time.RFC3339Nano)
	if !isFresh(recent, time.Now()) {
		t.Fatal("fresh timestamp treated as stale")
	}
}

var _ = filepath.Join
