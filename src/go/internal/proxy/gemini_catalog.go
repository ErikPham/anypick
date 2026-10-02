package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/egress"
	"github.com/outof0/anypick/go/internal/protocol/gemini"
)

// geminiCatalogTTL / geminiCatalogFailTTL mirror the TS MODEL_CATALOG_TTL_MS /
// MODEL_CATALOG_FAILURE_TTL_MS: a fresh catalog is reused for a while, and a
// failed fetch is not retried for a short window so a dead upstream does not
// spam ListModels on every request.
const (
	geminiCatalogTTL     = 10 * time.Minute
	geminiCatalogFailTTL = 30 * time.Second
)

type geminiCatalogEntry struct {
	value     *gemini.GeminiModelCatalog
	fetchedAt time.Time
	err       error
	errAt     time.Time
}

var (
	geminiCatalogMu    sync.Mutex
	geminiCatalogCache = map[string]*geminiCatalogEntry{}
)

func geminiCatalogKey(up *Upstream) string {
	if up == nil || up.Gemini == nil {
		return "unknown"
	}
	scope := strings.TrimSpace(up.CatalogScope)
	if scope != "" {
		scope += "|"
	}
	if up.Gemini.APIKeyMode {
		base := geminiUpstreamGoogle
		if up.Gemini.APIKeyBase != nil {
			base = strings.TrimSuffix(up.Gemini.APIKeyBase.String(), "/")
		}
		return scope + "api-key:" + base
	}
	base := geminiUpstreamGoogle
	if up.Gemini.CodeAssistBase != nil {
		base = strings.TrimSuffix(up.Gemini.CodeAssistBase.String(), "/")
	}
	return scope + "oauth:" + base
}

// liveGeminiCatalog is the best-effort accessor used by model resolution: a
// failed fetch yields nil, which callers treat as the empty-catalog fallback
// (resolveGeminiModel still passes concrete ids through).
func liveGeminiCatalog(ctx context.Context, up *Upstream) *gemini.GeminiModelCatalog {
	cat, _ := liveGeminiCatalogWithError(ctx, up)
	return cat
}

// liveGeminiCatalogWithError is the same cache with the failure reason
// attached. GET /v1/models needs it: the reference answers 502 rather than an
// empty list, because a client that enumerates models would rather see an
// honest failure than a list we made up. Ports liveModelCatalog (server.ts).
func liveGeminiCatalogWithError(ctx context.Context, up *Upstream) (*gemini.GeminiModelCatalog, error) {
	key := geminiCatalogKey(up)
	now := time.Now()
	geminiCatalogMu.Lock()
	if e, ok := geminiCatalogCache[key]; ok {
		if e.err == nil && now.Sub(e.fetchedAt) < geminiCatalogTTL {
			v := e.value
			geminiCatalogMu.Unlock()
			return v, nil
		}
		if e.err != nil && now.Sub(e.errAt) < geminiCatalogFailTTL {
			err := e.err
			geminiCatalogMu.Unlock()
			return nil, err
		}
	}
	geminiCatalogMu.Unlock()

	cat, err := refreshGeminiModelCatalog(ctx, up)
	geminiCatalogMu.Lock()
	if err != nil {
		geminiCatalogCache[key] = &geminiCatalogEntry{err: err, errAt: now}
	} else {
		geminiCatalogCache[key] = &geminiCatalogEntry{value: cat, fetchedAt: now}
	}
	geminiCatalogMu.Unlock()
	if err != nil {
		return nil, err
	}
	return cat, nil
}

// isListModels matches the OpenAI model-enumeration route.
//
// A Gemini upstream must answer it locally. Left to the passthrough branch the
// request is reverse-proxied to cloudcode-pa, which answers Google's HTML 404
// page: a client enumerating models gets markup where it expects JSON. The TS
// reference served this route (gemini-proxy/server.ts, handleListModels).
func isListModels(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		(r.URL.Path == "/v1/models" || r.URL.Path == "/models")
}

// serveGeminiModels answers GET /v1/models for a Gemini-backed upstream. Ports
// handleListModels: ids are de-duplicated, and a catalog miss is a 502 — never
// an invented list.
func serveGeminiModels(w http.ResponseWriter, r *http.Request, up *Upstream) {
	cat, err := liveGeminiCatalogWithError(r.Context(), up)
	if cat == nil {
		msg := "no Gemini model catalog available"
		if err != nil {
			msg = err.Error()
		}
		writeJSONError(w, http.StatusBadGateway, "proxy_error",
			"Failed to list Gemini models from Google: "+msg)
		return
	}
	// The TS reference labels API-key catalogs 'google' and OAuth catalogs
	// 'google-code-assist'; keep the distinction so clients can tell them apart.
	ownedBy := "google-code-assist"
	if up.Gemini != nil && up.Gemini.APIKeyMode {
		ownedBy = "google"
	}
	type modelEntry struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		OwnedBy     string `json:"owned_by"`
		DisplayName string `json:"display_name,omitempty"`
	}
	data := make([]modelEntry, 0, len(cat.Models))
	seen := make(map[string]struct{}, len(cat.Models))
	for _, m := range cat.Models {
		if m.ID == "" {
			continue
		}
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}
		data = append(data, modelEntry{
			ID: m.ID, Object: "model", OwnedBy: ownedBy, DisplayName: m.DisplayName,
		})
	}
	body, merr := json.Marshal(map[string]interface{}{"object": "list", "data": data})
	if merr != nil {
		writeJSONError(w, http.StatusInternalServerError, "proxy_error", "Failed to encode model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// refreshGeminiModelCatalog performs one live catalog fetch for the upstream's
// mode. Ports refreshModelCatalog (server.ts) without the in-flight dedup
// (the cache above prevents repeated fetches once populated).
func refreshGeminiModelCatalog(ctx context.Context, up *Upstream) (*gemini.GeminiModelCatalog, error) {
	if up.Gemini == nil {
		return nil, fmt.Errorf("gemini target missing")
	}
	if up.Gemini.APIKeyMode {
		token := up.Auth
		if up.AuthProvider != nil {
			got, perr := up.AuthProvider(ctx)
			if perr != nil {
				return nil, fmt.Errorf("gemini credential refresh failed: %w", perr)
			}
			token = got
		}
		if token == "" {
			return nil, fmt.Errorf("no Gemini API key for catalog fetch")
		}
		base := geminiUpstreamGoogle
		if up.Gemini.APIKeyBase != nil {
			base = strings.TrimSuffix(up.Gemini.APIKeyBase.String(), "/")
		}
		return fetchGeminiCatalogFromGoogle(ctx, base, token)
	}
	if up.AuthProvider == nil {
		return nil, fmt.Errorf("no Gemini OAuth provider for catalog fetch")
	}
	token, err := up.AuthProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("gemini oauth token: %w", err)
	}
	base := geminiUpstreamGoogle
	if up.Gemini.CodeAssistBase != nil {
		base = strings.TrimSuffix(up.Gemini.CodeAssistBase.String(), "/")
	}
	cat, primaryErr := fetchGeminiOAuthCatalog(ctx, base, token, up.Gemini.IDEType, up.Gemini.UserAgent)
	if primaryErr == nil {
		return cat, nil
	}
	// Gemini CLI and Antigravity can share one saved account while Google
	// accepts only one client identity for a given request. Use the same
	// alternate identity as generateContent when the primary catalog probe is
	// rejected, so a provider rollout or stale primary route cannot erase the
	// account's model options.
	if alt := up.Gemini.AltRoute; alt != nil && alt.AuthProvider != nil {
		altToken, altErr := alt.AuthProvider(ctx)
		if altErr == nil {
			altBase := geminiUpstreamGoogle
			if alt.CodeAssistBase != nil {
				altBase = strings.TrimSuffix(alt.CodeAssistBase.String(), "/")
			}
			if altCat, fetchErr := fetchGeminiOAuthCatalog(
				ctx, altBase, altToken, alt.IDEType, alt.UserAgent); fetchErr == nil {
				return altCat, nil
			} else {
				return nil, fmt.Errorf("gemini code assist catalog: primary: %v; fallback: %w", primaryErr, fetchErr)
			}
		}
	}
	return nil, fmt.Errorf("gemini code assist catalog: %w", primaryErr)
}

func fetchGeminiOAuthCatalog(ctx context.Context, base, token, ideType, userAgent string) (*gemini.GeminiModelCatalog, error) {
	project, err := resolveCodeAssistProject(ctx, base, token, ideType)
	if err != nil {
		return nil, fmt.Errorf("gemini code assist project: %w", err)
	}
	return fetchGeminiCatalogFromCodeAssist(ctx, base, token, project, userAgent)
}

// fetchGeminiCatalogFromGoogle pages through Google Generative Language
// ListModels and returns bare ids that support generateContent. Ports
// listGeminiModelsFromGoogle (server.ts).
func fetchGeminiCatalogFromGoogle(ctx context.Context, base, apiKey string) (*gemini.GeminiModelCatalog, error) {
	client := egress.NewClient(15 * time.Second)
	ids := make([]string, 0)
	seen := map[string]bool{}
	pageToken := ""
	pages := 0
	const maxPages = 20
	for {
		u := base + "/v1beta/models"
		q := url.Values{}
		q.Set("pageSize", "100")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		// The key travels as a header only. Built into the URL it would appear
		// verbatim in every *url.Error this call can return, and those errors
		// reach user-visible boundaries (MODEL_REQUIRED, `anypick models`,
		// desktop logs, the /v1/models 502 body).
		req.Header.Set("x-goog-api-key", apiKey)
		req.Header.Set("accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("google listmodels: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("google listmodels http %d: %s", resp.StatusCode, truncate(string(raw), 200))
		}
		var body struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("decode google listmodels: %w", err)
		}
		for _, m := range body.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if id == "" || seen[id] {
				continue
			}
			if len(m.SupportedGenerationMethods) > 0 &&
				!containsString(m.SupportedGenerationMethods, "generateContent") {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		pageToken = strings.TrimSpace(body.NextPageToken)
		pages++
		if pageToken == "" || pages >= maxPages {
			break
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return gemini.CompareGeminiModelIDs(ids[i], ids[j]) < 0 // newest first
	})
	models := make([]gemini.GeminiModelDescriptor, 0, len(ids))
	for _, id := range ids {
		models = append(models, gemini.GeminiModelDescriptor{ID: id})
	}
	return &gemini.GeminiModelCatalog{Models: models}, nil
}

// codeAssistCatalogResponse is the fetchAvailableModels payload.
type codeAssistCatalogResponse struct {
	Models              map[string]codeAssistModelDetails `json:"models"`
	DefaultAgentModelID string                            `json:"defaultAgentModelId"`
	AgentModelSorts     []struct {
		Groups []struct {
			ModelIDs []interface{} `json:"modelIds"`
		} `json:"groups"`
	} `json:"agentModelSorts"`
}

type codeAssistModelDetails struct {
	DisplayName   interface{} `json:"displayName"`
	Recommended   interface{} `json:"recommended"`
	Disabled      interface{} `json:"disabled"`
	APIProvider   interface{} `json:"apiProvider"`
	ModelProvider interface{} `json:"modelProvider"`
}

// fetchGeminiCatalogFromCodeAssist discovers the account-scoped catalog via
// Code Assist fetchAvailableModels. Ports listGeminiModelsFromCodeAssist
// (server.ts), including the retrieveUserQuota fallback when fetchAvailableModels
// is denied.
func fetchGeminiCatalogFromCodeAssist(ctx context.Context, base, token, project, userAgent string) (*gemini.GeminiModelCatalog, error) {
	client := egress.NewClient(30 * time.Second)
	u := base + "/v1internal:fetchAvailableModels"
	payload, _ := json.Marshal(map[string]string{"project": project})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	if userAgent != "" {
		req.Header.Set("user-agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("code assist fetchAvailableModels: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Older Gemini CLI entitlements can deny fetchAvailableModels; fall
		// back to quota metadata (real ids, no availability proof).
		return fetchGeminiCatalogFromCodeAssistQuota(ctx, client, base, token, project, string(raw))
	}
	var body codeAssistCatalogResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode code assist catalog: %w", err)
	}
	return parseCodeAssistCatalog(body), nil
}

func parseCodeAssistCatalog(body codeAssistCatalogResponse) *gemini.GeminiModelCatalog {
	byID := map[string]gemini.GeminiModelDescriptor{}
	rawToCanonical := map[string]string{}
	available := map[string]bool{}
	for rawID, details := range body.Models {
		id := strings.TrimPrefix(strings.TrimSpace(rawID), "models/")
		if id == "" || isTrue(details.Disabled) {
			continue
		}
		if s, ok := details.APIProvider.(string); ok && s != "" && s != "API_PROVIDER_GOOGLE_GEMINI" {
			continue
		}
		if s, ok := details.ModelProvider.(string); ok && s != "" && s != "MODEL_PROVIDER_GOOGLE" {
			continue
		}
		canonicalID := codeAssistPublicModelID(id)
		d := gemini.GeminiModelDescriptor{ID: canonicalID, UpstreamID: id}
		if s, ok := details.DisplayName.(string); ok && s != "" {
			d.DisplayName = codeAssistPublicDisplayName(s)
		}
		if isTrue(details.Recommended) {
			d.Recommended = true
		}
		if existing, ok := byID[canonicalID]; !ok || preferCodeAssistModel(d, existing) {
			byID[canonicalID] = d
		}
		rawToCanonical[id] = canonicalID
		available[canonicalID] = true
	}
	if len(byID) == 0 {
		return &gemini.GeminiModelCatalog{}
	}
	models := make([]gemini.GeminiModelDescriptor, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	preferred := make([]string, 0)
	for _, sort := range body.AgentModelSorts {
		for _, group := range sort.Groups {
			for _, mid := range group.ModelIDs {
				if s, ok := mid.(string); ok {
					raw := strings.TrimPrefix(strings.TrimSpace(s), "models/")
					id := rawToCanonical[raw]
					if id == "" {
						id = codeAssistPublicModelID(raw)
					}
					if id != "" && available[id] && !containsString(preferred, id) {
						preferred = append(preferred, id)
					}
				}
			}
		}
	}
	out := &gemini.GeminiModelCatalog{Models: models, PreferredModelIDs: preferred}
	defaultRaw := strings.TrimPrefix(strings.TrimSpace(body.DefaultAgentModelID), "models/")
	defaultID := rawToCanonical[defaultRaw]
	if defaultID == "" {
		defaultID = codeAssistPublicModelID(defaultRaw)
	}
	if defaultID != "" && available[defaultID] {
		out.DefaultModelID = defaultID
	}
	return out
}

// codeAssistPublicModelID hides the Code Assist-only "-tiered" route suffix
// from clients while descriptors keep the original as UpstreamID for egress.
func codeAssistPublicModelID(id string) string {
	id = strings.TrimSpace(id)
	lower := strings.ToLower(id)
	if strings.HasPrefix(lower, "gemini-") && strings.HasSuffix(lower, "-tiered") {
		return id[:len(id)-len("-tiered")]
	}
	return id
}

func codeAssistPublicDisplayName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	for _, suffix := range []string{" (tiered)", " tiered"} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)])
		}
	}
	return trimmed
}

func preferCodeAssistModel(candidate, existing gemini.GeminiModelDescriptor) bool {
	// An exact public ID should win over an internal tiered alias when both
	// are advertised. Otherwise the tiered route keeps its internal ID solely
	// for the upstream request while clients see the canonical family name.
	candidateCanonical := candidate.ID == candidate.UpstreamID
	existingCanonical := existing.ID == existing.UpstreamID
	if candidateCanonical != existingCanonical {
		return candidateCanonical
	}
	return candidate.Recommended && !existing.Recommended
}

// fetchGeminiCatalogFromCodeAssistQuota is the partial fallback when
// fetchAvailableModels is denied: retrieveUserQuota returns real ids but never
// proves another explicit id is unavailable. Ports listGeminiModelsFromCodeAssistQuota.
func fetchGeminiCatalogFromCodeAssistQuota(ctx context.Context, client *http.Client, base, token, project, catalogError string) (*gemini.GeminiModelCatalog, error) {
	u := base + "/v1internal:retrieveUserQuota"
	payload, _ := json.Marshal(map[string]string{"project": project, "userAgent": "anypick-gemini-proxy"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("code assist retrieveUserQuota: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("code assist model discovery failed: fetchAvailableModels %s; retrieveUserQuota http %d: %s",
			truncate(catalogError, 120), resp.StatusCode, truncate(string(raw), 200))
	}
	var body struct {
		Buckets []struct {
			ModelID interface{} `json:"modelId"`
		} `json:"buckets"`
		Models []interface{} `json:"models"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode code assist quota: %w", err)
	}
	seen := map[string]bool{}
	models := make([]gemini.GeminiModelDescriptor, 0)
	add := func(v interface{}) {
		s, ok := v.(string)
		if !ok {
			return
		}
		id := strings.TrimPrefix(strings.TrimSpace(s), "models/")
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		models = append(models, gemini.GeminiModelDescriptor{ID: id, UpstreamID: id})
	}
	for _, b := range body.Buckets {
		add(b.ModelID)
	}
	for _, m := range body.Models {
		switch v := m.(type) {
		case string:
			add(v)
		case map[string]interface{}:
			add(v["id"])
			add(v["name"])
		}
	}
	return &gemini.GeminiModelCatalog{Models: normalizeCodeAssistCatalogModels(models)}, nil
}

func normalizeCodeAssistCatalogModels(rawModels []gemini.GeminiModelDescriptor) []gemini.GeminiModelDescriptor {
	byID := make(map[string]gemini.GeminiModelDescriptor, len(rawModels))
	for _, raw := range rawModels {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			continue
		}
		canonicalID := codeAssistPublicModelID(id)
		raw.ID = canonicalID
		if strings.TrimSpace(raw.UpstreamID) == "" {
			raw.UpstreamID = id
		}
		raw.DisplayName = codeAssistPublicDisplayName(raw.DisplayName)
		if existing, ok := byID[canonicalID]; !ok || preferCodeAssistModel(raw, existing) {
			byID[canonicalID] = raw
		}
	}
	out := make([]gemini.GeminiModelDescriptor, 0, len(byID))
	for _, model := range byID {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func isTrue(v interface{}) bool {
	b, ok := v.(bool)
	return ok && b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
