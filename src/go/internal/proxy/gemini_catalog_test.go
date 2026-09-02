package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/protocol/gemini"
)

func TestParseCodeAssistCatalog(t *testing.T) {
	body := codeAssistCatalogResponse{
		Models: map[string]codeAssistModelDetails{
			"models/gemini-2.5-pro":      {DisplayName: "Gemini 2.5 Pro", Recommended: true},
			"models/gemini-2.5-flash":    {DisplayName: "Gemini 2.5 Flash"},
			"models/gemini-2.0-flash":    {DisplayName: "Gemini 2.0 Flash"},
			"models/disabled-model":      {Disabled: true},
			"models/non-google":          {APIProvider: "API_PROVIDER_ANTHROPIC"},
			"models/non-google-provider": {ModelProvider: "MODEL_PROVIDER_OPENAI"},
		},
		DefaultAgentModelID: "models/gemini-2.5-pro",
		AgentModelSorts: []struct {
			Groups []struct {
				ModelIDs []interface{} `json:"modelIds"`
			} `json:"groups"`
		}{
			{Groups: []struct {
				ModelIDs []interface{} `json:"modelIds"`
			}{
				{ModelIDs: []interface{}{"models/gemini-2.5-flash", "models/gemini-2.0-flash"}},
			}},
		},
	}
	cat := parseCodeAssistCatalog(body)
	if cat.DefaultModelID != "gemini-2.5-pro" {
		t.Fatalf("default = %q", cat.DefaultModelID)
	}
	if len(cat.PreferredModelIDs) != 2 ||
		cat.PreferredModelIDs[0] != "gemini-2.5-flash" || cat.PreferredModelIDs[1] != "gemini-2.0-flash" {
		t.Fatalf("preferred = %v", cat.PreferredModelIDs)
	}
	ids := map[string]bool{}
	for _, m := range cat.Models {
		ids[m.ID] = true
		if m.ID == "disabled-model" || m.ID == "non-google" || m.ID == "non-google-provider" {
			t.Fatalf("filtered model leaked: %q", m.ID)
		}
	}
	for _, want := range []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"} {
		if !ids[want] {
			t.Fatalf("missing model %q (got %v)", want, ids)
		}
	}
	// Map iteration order is nondeterministic, so look up by id.
	var pro *gemini.GeminiModelDescriptor
	for i := range cat.Models {
		if cat.Models[i].ID == "gemini-2.5-pro" {
			pro = &cat.Models[i]
			break
		}
	}
	if pro == nil {
		t.Fatalf("gemini-2.5-pro missing from catalog: %v", ids)
	}
	if !pro.Recommended {
		t.Fatalf("gemini-2.5-pro should be recommended: %+v", *pro)
	}
}

func TestRefreshGeminiCatalogFromGoogle(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "models/gemini-2.5-pro", "supportedGenerationMethods": []string{"generateContent"}},
				{"name": "models/gemini-1.5-flash", "supportedGenerationMethods": []string{"generateContent", "embedContent"}},
				{"name": "models/text-embedding-004", "supportedGenerationMethods": []string{"embedContent"}},
			},
		})
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	up := &Upstream{Gemini: &GeminiTarget{APIKeyMode: true, APIKeyBase: base}, Auth: "test-key"}
	cat, err := refreshGeminiModelCatalog(context.Background(), up)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("listmodels hits = %d, want 1", hits)
	}
	ids := map[string]bool{}
	for _, m := range cat.Models {
		ids[m.ID] = true
	}
	if !ids["gemini-2.5-pro"] || !ids["gemini-1.5-flash"] {
		t.Fatalf("expected generateContent models, got %v", ids)
	}
	if ids["text-embedding-004"] {
		t.Fatalf("embedding model (no generateContent) should be filtered: %v", ids)
	}
}

func TestLiveGeminiCatalogCachesAndFallsBack(t *testing.T) {
	geminiCatalogMu.Lock()
	geminiCatalogCache = map[string]*geminiCatalogEntry{}
	geminiCatalogMu.Unlock()

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "models/gemini-2.5-pro", "supportedGenerationMethods": []string{"generateContent"}},
			},
		})
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	up := &Upstream{Gemini: &GeminiTarget{APIKeyMode: true, APIKeyBase: base}, Auth: "k"}

	// First call fetches.
	if c := liveGeminiCatalog(context.Background(), up); c == nil || len(c.Models) != 1 {
		t.Fatalf("first call returned %+v", c)
	}
	if hits != 1 {
		t.Fatalf("hits after first = %d, want 1", hits)
	}
	// Second call reuses the cache (TTL), no second upstream hit.
	if c := liveGeminiCatalog(context.Background(), up); c == nil || len(c.Models) != 1 {
		t.Fatalf("second call returned %+v", c)
	}
	if hits != 1 {
		t.Fatalf("hits after second (cached) = %d, want 1", hits)
	}

	// Failure path: a dead upstream returns nil and is not retried within the
	// failure TTL.
	geminiCatalogMu.Lock()
	geminiCatalogCache = map[string]*geminiCatalogEntry{}
	geminiCatalogMu.Unlock()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	deadBase, _ := url.Parse(dead.URL)
	deadUp := &Upstream{Gemini: &GeminiTarget{APIKeyMode: true, APIKeyBase: deadBase}, Auth: "k"}
	if c := liveGeminiCatalog(context.Background(), deadUp); c != nil {
		t.Fatalf("failed fetch should return nil, got %+v", c)
	}
	// Within failure TTL, the next call must NOT hit the upstream again.
	if c := liveGeminiCatalog(context.Background(), deadUp); c != nil {
		t.Fatalf("failed fetch (cached) should return nil, got %+v", c)
	}
	if hits != 2 { // 1 success-server hit + 1 dead-server hit; cached failures don't add
		t.Fatalf("hits = %d, want 2 (cached failure must not re-hit)", hits)
	}
}
