package catalog

// Vendor model-list requests, ported from src/catalog/model-fetch.ts. The
// wire shape belongs to the vendor, not to core; provider ids never appear in
// a switch outside this file and the policy table above.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/egress"
)

const fetchTimeout = 6 * time.Second

var versionedSuffix = regexp.MustCompile(`/v\d+(beta)?$`)

func trimBase(endpoint string) string { return strings.TrimRight(endpoint, "/") }

// parseModelIds handles the three list shapes in circulation: OpenAI's
// data[].id, Anthropic's data[].id, and Google's models[].name (path
// qualified). A bare models: string[] also appears on self-hosted gateways.
func parseModelIds(body []byte) []string {
	var shape struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if json.Unmarshal(body, &shape) != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	push := func(raw string) {
		id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "models/"))
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, list := range []json.RawMessage{shape.Data, shape.Models} {
		if len(list) == 0 {
			continue
		}
		var arr []json.RawMessage
		if json.Unmarshal(list, &arr) != nil {
			continue
		}
		for _, entry := range arr {
			var s string
			if json.Unmarshal(entry, &s) == nil && s != "" {
				push(s)
				continue
			}
			var item struct {
				ID   *string `json:"id"`
				Name *string `json:"name"`
			}
			if json.Unmarshal(entry, &item) == nil {
				if item.ID != nil {
					push(*item.ID)
				} else if item.Name != nil {
					push(*item.Name)
				}
			}
		}
	}
	return out
}

// FetchContext is the input every live fetch needs.
type FetchContext struct {
	Endpoint        string
	APIKey          string
	AuthHeader      string
	AuthValuePrefix string
	Client          *http.Client
}

func (c *FetchContext) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return egress.NewClient(fetchTimeout)
}

func (c *FetchContext) setAuth(req *http.Request, defaultHeader, defaultPrefix string) {
	if c.APIKey == "" {
		return
	}
	header := c.AuthHeader
	if header == "" {
		header = defaultHeader
	}
	prefix := c.AuthValuePrefix
	if prefix == "" && c.AuthHeader == "" {
		prefix = defaultPrefix
	}
	req.Header.Set(header, prefix+c.APIKey)
}

func readModelIDs(resp *http.Response) ([]string, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A model list is advisory: callers fall back to the static catalog.
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseModelIds(body), nil
}

// FetchLiveModels asks the vendor for its current model list using the wire
// shape named by the provider's policy. Ordering is preserved — vendors list
// newest first and pickers show the first match as default.
func FetchLiveModels(ctx context.Context, style string, fc *FetchContext) ([]string, error) {
	switch style {
	case "openai":
		return fetchOpenAiStyleModels(ctx, fc)
	case "anthropic":
		return fetchAnthropicModels(ctx, fc)
	case "gemini":
		return fetchGeminiModels(ctx, fc)
	default:
		return nil, fmt.Errorf("no live model discovery for style %q", style)
	}
}

func fetchOpenAiStyleModels(ctx context.Context, fc *FetchContext) ([]string, error) {
	base := trimBase(fc.Endpoint)
	target := base + "/v1/models"
	if versionedSuffix.MatchString(base) {
		target = base + "/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("x-opencode-client", "desktop")
	req.Header.Set("user-agent", "opencode/desktop")
	fc.setAuth(req, "authorization", "Bearer ")
	resp, err := fc.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	return readModelIDs(resp)
}

func fetchAnthropicModels(ctx context.Context, fc *FetchContext) ([]string, error) {
	if fc.APIKey == "" {
		return nil, nil
	}
	base := trimBase(fc.Endpoint)
	target := base + "/v1/models"
	if versionedSuffix.MatchString(base) {
		target = base + "/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	fc.setAuth(req, "x-api-key", "")
	req.Header.Set("anthropic-version", AnthropicVersion())
	resp, err := fc.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	return readModelIDs(resp)
}

// fetchGeminiModels puts the key in the query string and follows bounded
// pagination: a vendor echoing pageToken would otherwise spin forever.
func fetchGeminiModels(ctx context.Context, fc *FetchContext) ([]string, error) {
	if fc.APIKey == "" {
		return nil, nil
	}
	base := trimBase(fc.Endpoint)
	root := base + "/v1beta"
	if versionedSuffix.MatchString(base) {
		root = base
	}
	var ids []string
	pageToken := ""
	for page := 0; page < 5; page++ {
		u, err := url.Parse(root + "/models")
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("key", fc.APIKey)
		q.Set("pageSize", "200")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u.RawQuery = q.Encode()
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if rerr != nil {
			return nil, rerr
		}
		req.Header.Set("accept", "application/json")
		resp, derr := fc.httpClient().Do(req)
		if derr != nil {
			return nil, derr
		}
		body, rerr2 := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if rerr2 != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			break
		}
		var parsed struct {
			NextPageToken string `json:"nextPageToken"`
		}
		ids = append(ids, parseModelIds(body)...)
		if json.Unmarshal(body, &parsed) != nil || parsed.NextPageToken == "" {
			break
		}
		pageToken = parsed.NextPageToken
	}
	return dedupe(ids), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
