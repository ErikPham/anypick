package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrModelDiscoveryUnsupported means the resolved upstream has no provider-
// owned catalog operation. Static catalog policy remains the caller's safe
// fallback; this is not a proxy-start failure.
var ErrModelDiscoveryUnsupported = errors.New("upstream does not support model discovery")

// DiscoverModels executes one provider-owned live catalog operation. Gemini's
// OAuth/API-key implementation is already part of the proxy transport, so it
// is selected by the resolved capability rather than by an account-provider
// switch in the desktop layer.
func DiscoverModels(ctx context.Context, up *Upstream) ([]string, error) {
	if up == nil {
		return nil, ErrModelDiscoveryUnsupported
	}
	if up.ModelDiscovery != nil {
		models, err := up.ModelDiscovery(ctx)
		if err != nil {
			return nil, err
		}
		return normalizeDiscoveredModels(models)
	}
	if up.Gemini == nil {
		return nil, ErrModelDiscoveryUnsupported
	}

	// Route through the same TTL cache the serving path uses: a discovery
	// call now also warms GET /v1/models instead of issuing one live Google
	// request per account save, model list, or activation.
	cat, err := liveGeminiCatalogWithError(ctx, up)
	if err != nil {
		return nil, err
	}
	if cat == nil {
		return nil, fmt.Errorf("provider returned no model catalog")
	}
	models := make([]string, 0, len(cat.Models))
	for _, model := range cat.Models {
		models = append(models, model.ID)
	}
	return normalizeDiscoveredModels(models)
}

func normalizeDiscoveredModels(models []string) ([]string, error) {
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, raw := range models {
		id := strings.TrimSpace(strings.TrimPrefix(raw, "models/"))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("provider returned an empty model catalog")
	}
	return out, nil
}

// serveDiscoveredModels answers GET /v1/models for an upstream whose provider
// owns a catalog operation. The proxy must publish every id it can actually
// serve: a client that enumerates models here has to see the same list AnyPick
// can bind, otherwise it falls back to ids the vendor may reject. Discovery
// failure is a 502 — never an invented list.
func serveDiscoveredModels(w http.ResponseWriter, r *http.Request, up *Upstream) {
	models, err := DiscoverModels(r.Context(), up)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "proxy_error",
			"Failed to list upstream models: "+err.Error())
		return
	}
	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]modelEntry, 0, len(models))
	for _, id := range models {
		data = append(data, modelEntry{ID: id, Object: "model", OwnedBy: "anypick"})
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
