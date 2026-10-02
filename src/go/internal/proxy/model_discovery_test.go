package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDiscoverModelsGeminiReadsLiveCatalog(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "models/gemini-9.9-flash", "supportedGenerationMethods": []string{"generateContent"}},
				{"name": "models/embedding-new", "supportedGenerationMethods": []string{"embedContent"}},
			},
		})
	}))
	defer srv.Close()

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	up := &Upstream{
		Auth:         "test-gemini-key",
		CatalogScope: "account/gemini/antigravity",
		Gemini:       &GeminiTarget{APIKeyMode: true, APIKeyBase: base},
	}
	models, err := DiscoverModels(context.Background(), up)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "test-gemini-key" {
		t.Fatalf("x-goog-api-key = %q", gotKey)
	}
	if len(models) != 1 || models[0] != "gemini-9.9-flash" {
		t.Fatalf("models = %v, want the live model only", models)
	}
	geminiCatalogMu.Lock()
	_, cached := geminiCatalogCache[geminiCatalogKey(up)]
	geminiCatalogMu.Unlock()
	if !cached {
		t.Fatal("live Gemini catalog was not retained for the account scope")
	}
}

// The proxy must enumerate exactly the ids it can serve for a provider-owned
// catalog — including vendor tier slots like WorkBuddy's "Auto"
// (default-model): clients pick from this list, so hiding an id AnyPick can
// bind would send them to a model they cannot use.
func TestServeListsDiscoveredUpstreamModels(t *testing.T) {
	up := &Upstream{
		BaseURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		Auth:    "tok",
		ModelDiscovery: func(context.Context) ([]string, error) {
			return []string{"default-model", "deepseek-v4.1-flash", "kimi-k3", "deepseek-v4.1-flash"}, nil
		},
	}
	rec := httptest.NewRecorder()
	Serve(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil), up)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "list" {
		t.Fatalf("object = %q, want list", got.Object)
	}
	ids := make([]string, 0, len(got.Data))
	for _, entry := range got.Data {
		ids = append(ids, entry.ID)
	}
	want := "default-model,deepseek-v4.1-flash,kimi-k3"
	if gotIDs := strings.Join(ids, ","); gotIDs != want {
		t.Fatalf("ids = %q, want %q", gotIDs, want)
	}
	for _, entry := range got.Data {
		if entry.Object != "model" || entry.OwnedBy != "anypick" {
			t.Fatalf("entry shape = %+v", entry)
		}
	}
}

// A provider-owned catalog that cannot be read is a 502, never an invented
// list: the caller must see the failure, not a plausible-looking route.
func TestServeDiscoveredModelsFailureIs502(t *testing.T) {
	up := &Upstream{
		BaseURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		Auth:    "tok",
		ModelDiscovery: func(context.Context) ([]string, error) {
			return nil, errors.New("product catalog not found")
		},
	}
	rec := httptest.NewRecorder()
	Serve(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil), up)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "product catalog not found") {
		t.Fatalf("failure reason missing from body: %s", rec.Body.String())
	}
}

// The per-account Server rebuilds its egress Upstream field by field
// (upstreamValue); a dropped ModelDiscovery silently turns GET /v1/models back
// into vendor passthrough — a 404 from the vendor instead of the ids this
// proxy can serve. Drive the full authenticated Handler so the wiring, not
// just the leaf, stays honest.
func TestServerHandlerCarriesModelDiscovery(t *testing.T) {
	up := &Upstream{
		BaseURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
		Auth:    "up-token",
		ModelDiscovery: func(context.Context) ([]string, error) {
			return []string{"default-model", "deepseek-v4.1-flash"}, nil
		},
	}
	srv := (&Service{}).buildServer("workbuddy", "work", mustNewInstanceID(t), "proxy-token", up)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer proxy-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"default-model", "deepseek-v4.1-flash"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("catalog missing %q: %s", want, rec.Body.String())
		}
	}
}
