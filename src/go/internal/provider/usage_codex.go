package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexLiveUsage fetches the ChatGPT rate-limit snapshot using the live
// ~/.codex/auth.json OAuth token.
func (p *codex) LiveUsage(ctx context.Context) (*LiveUsage, error) {
	raw, err := os.ReadFile(p.authPath())
	if err != nil {
		return nil, err
	}
	var auth struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("no access token")
	}

	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://chatgpt.com/backend-api/wham/usage", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage endpoint status %d", resp.StatusCode)
	}
	return parseCodexUsage(body)
}

// parseCodexUsage mirrors parseCodexUsage in src/providers/codex-usage.ts:
// reads primary_window/secondary_window used_percent → remaining percent.
func parseCodexUsage(body []byte) (*LiveUsage, error) {
	var root map[string]interface{}
	if json.Unmarshal(body, &root) != nil {
		return nil, fmt.Errorf("invalid usage JSON")
	}
	rateLimit := objectMap(root["rate_limit"])
	if rateLimit == nil {
		rateLimit = objectMap(root["rate_limits"])
	}
	if rateLimit == nil {
		rateLimit = root
	}

	var windows []UsageWindow
	for _, pair := range [][2]string{
		{"primary_window", "primary"},
		{"secondary_window", "secondary"},
	} {
		window := objectMap(rateLimit[pair[0]])
		if window == nil {
			continue
		}
		used := numberValue(window["used_percent"])
		if math.IsNaN(used) {
			continue
		}
		w := UsageWindow{
			Label:            usageLabel(pair[1], window),
			RemainingPercent: clampPercent(100 - used),
		}
		if ms := resetAtMs(window); ms != nil {
			w.ResetsAtMs = ms
		}
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no usage windows")
	}
	return &LiveUsage{Windows: windows}, nil
}

func objectMap(v interface{}) map[string]interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	return m
}

func numberValue(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return math.NaN()
	}
}

func resetAtMs(window map[string]interface{}) *float64 {
	for _, key := range []string{"resets_at_ms", "reset_at_ms"} {
		if v, ok := window[key].(float64); ok && !math.IsNaN(v) {
			return &v
		}
	}
	return nil
}

// usageLabel returns a human label for the usage window, preferring the
// vendor-provided label when present (e.g. a plan name).
func usageLabel(fallback string, window map[string]interface{}) string {
	if label, ok := window["label"].(string); ok && strings.TrimSpace(label) != "" {
		return strings.TrimSpace(label)
	}
	return fallback
}

func clampPercent(v float64) int {
	return int(math.Max(0, math.Min(100, math.Round(v))))
}

var _ = filepath.Join
