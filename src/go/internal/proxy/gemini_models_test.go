package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/protocol/gemini"
)

// These tests exist because GET /v1/models had no inbound handler for a Gemini
// upstream. The request fell through to kindPassthrough and was
// reverse-proxied to cloudcode-pa, which answers Google's HTML 404: a client
// enumerating models received markup where it expected JSON. The TS reference
// served the route locally (gemini-proxy/server.ts, handleListModels), so the
// Go port must too.

func TestGeminiServeAnswersListModelsLocally(t *testing.T) {
	geminiCatalogMu.Lock()
	geminiCatalogCache = map[string]*geminiCatalogEntry{}
	geminiCatalogMu.Unlock()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1beta/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-goog-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.RawQuery, "key=") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "models/gemini-2.5-pro", "supportedGenerationMethods": []string{"generateContent"}},
				{"name": "models/gemini-1.5-flash", "supportedGenerationMethods": []string{"generateContent"}},
				{"name": "models/text-embedding-004", "supportedGenerationMethods": []string{"embedContent"}},
			},
		})
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	up := &Upstream{
		BaseURL:         base,
		Auth:            "test-key",
		TranslateGemini: true,
		Gemini:          &GeminiTarget{APIKeyMode: true, APIKeyBase: base},
	}
	rec := httptest.NewRecorder()
	Serve(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil), up)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json — a passthrough to Google would serve text/html here", ct)
	}
	if hits != 1 {
		t.Fatalf("upstream listmodels hits = %d, want 1", hits)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID          string `json:"id"`
			Object      string `json:"object"`
			OwnedBy     string `json:"owned_by"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON (%v): %q", err, rec.Body.String())
	}
	if body.Object != "list" {
		t.Fatalf("object = %q, want \"list\"", body.Object)
	}
	byID := map[string]string{}
	for _, m := range body.Data {
		if m.Object != "model" {
			t.Fatalf("entry %q object = %q, want \"model\"", m.ID, m.Object)
		}
		byID[m.ID] = m.OwnedBy
	}
	for _, want := range []string{"gemini-2.5-pro", "gemini-1.5-flash"} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("missing generateContent model %q: %v", want, byID)
		}
	}
	if _, bad := byID["text-embedding-004"]; bad {
		t.Fatalf("embedding-only model leaked into the list: %v", byID)
	}
	for id, owned := range byID {
		if owned != "google" {
			t.Fatalf("model %q owned_by = %q, want \"google\" (API-key catalog)", id, owned)
		}
	}
}

// An OAuth (Code Assist) catalog is labelled 'google-code-assist', and the
// list is de-duplicated: fetchAvailableModels can return the same id twice and
// a client that renders one row per entry would show it twice.
func TestGeminiListModelsDeduplicatesAndLabelsOAuthCatalogs(t *testing.T) {
	geminiCatalogMu.Lock()
	geminiCatalogCache = map[string]*geminiCatalogEntry{}
	geminiCatalogMu.Unlock()

	base, _ := url.Parse("https://cloudcode-pa.example.test")
	up := &Upstream{
		BaseURL: base,
		Gemini:  &GeminiTarget{CodeAssistBase: base},
		AuthProvider: func(ctx context.Context) (string, error) {
			return "tok", nil
		},
	}
	// Seed the cache so the assertion is about the served list, not about
	// live discovery: a duplicate id and an empty id both reach the handler.
	geminiCatalogMu.Lock()
	geminiCatalogCache[geminiCatalogKey(up)] = &geminiCatalogEntry{
		value: &gemini.GeminiModelCatalog{Models: []gemini.GeminiModelDescriptor{
			{ID: "gemini-3.1-pro-low", DisplayName: "Gemini 3.1 Pro"},
			{ID: "gemini-3.1-pro-low"},
			{ID: ""},
			{ID: "gemini-2.5-flash"},
		}},
		fetchedAt: time.Now(),
	}
	geminiCatalogMu.Unlock()

	rec := httptest.NewRecorder()
	Serve(rec, httptest.NewRequest(http.MethodGet, "/models", nil), up)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID          string `json:"id"`
			OwnedBy     string `json:"owned_by"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON (%v): %q", err, rec.Body.String())
	}
	if len(body.Data) != 2 {
		t.Fatalf("entries = %d, want 2 after de-dup/empty-skip: %+v", len(body.Data), body.Data)
	}
	if body.Data[0].ID != "gemini-3.1-pro-low" || body.Data[1].ID != "gemini-2.5-flash" {
		t.Fatalf("order/ids = %+v", body.Data)
	}
	if body.Data[0].OwnedBy != "google-code-assist" {
		t.Fatalf("owned_by = %q, want \"google-code-assist\" (OAuth catalog)", body.Data[0].OwnedBy)
	}
	if body.Data[0].DisplayName != "Gemini 3.1 Pro" {
		t.Fatalf("display_name = %q, want it carried through", body.Data[0].DisplayName)
	}
}

// A catalog we cannot fetch must be an honest failure, never an invented list:
// a client that enumerates models and picks the first one would otherwise
// silently talk to a model id we made up. Ports the 502 in handleListModels.
func TestGeminiListModelsFailsHonestlyOnCatalogMiss(t *testing.T) {
	geminiCatalogMu.Lock()
	geminiCatalogCache = map[string]*geminiCatalogEntry{}
	geminiCatalogMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	up := &Upstream{
		BaseURL:         base,
		Auth:            "test-key",
		TranslateGemini: true,
		Gemini:          &GeminiTarget{APIKeyMode: true, APIKeyBase: base},
	}
	rec := httptest.NewRecorder()
	Serve(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil), up)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON (%v): %q", err, rec.Body.String())
	}
	if body.Error.Type != "proxy_error" {
		t.Fatalf("error type = %q, want proxy_error", body.Error.Type)
	}
	const want = "Failed to list Gemini models from Google:"
	if len(body.Error.Message) < len(want) || body.Error.Message[:len(want)] != want {
		t.Fatalf("message = %q, want prefix %q", body.Error.Message, want)
	}
}

// Every non-Gemini upstream keeps the previous behaviour: the route is still
// reverse-proxied, because that is where the vendor's own model list lives.
// The new branch must not silently swallow those.
func TestListModelsStaysPassthroughForNonGeminiUpstreams(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"vendor-model"}]}`))
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	up := &Upstream{BaseURL: base, Auth: "k"}
	rec := httptest.NewRecorder()
	Serve(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil), up)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/models" {
		t.Fatalf("upstream saw path %q, want /v1/models (request must still be forwarded)", gotPath)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("expected the vendor JSON body verbatim, got %q", rec.Body.String())
	}
}

func TestIsListModelsRouteMatching(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/v1/models", true},
		{http.MethodGet, "/models", true},
		{http.MethodHead, "/v1/models", true},
		{http.MethodPost, "/v1/models", false},
		{http.MethodGet, "/v1/models/gemini-2.5-pro", false},
		{http.MethodGet, "/v1/chat/completions", false},
		{http.MethodGet, "/health", false},
	}
	for _, c := range cases {
		got := isListModels(httptest.NewRequest(c.method, c.path, nil))
		if got != c.want {
			t.Errorf("isListModels(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
