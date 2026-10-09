package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// openCodeUsageEndpoint is a variable so tests can point the read at a
// loopback server instead of opencode.ai.
var openCodeUsageEndpoint = "https://opencode.ai/zen/go/v1/usage"

// LiveUsage asks the Zen/Go gateway for the subscription's rolling/weekly/
// monthly windows. The key comes from the same resolution the proxy uses
// (live CLI store, then a mirror), and an OAuth-only login has no platform
// key to read a quota with.
func (p *opencode) LiveUsage(ctx context.Context) (*LiveUsage, error) {
	key, _, _ := openCodeGatewayKey(p.dataDir())
	if key == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeUsageEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{
		Timeout: 5 * time.Second,
		// A redirect must not carry the platform key to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opencode usage HTTP %d", resp.StatusCode)
	}
	return parseOpenCodeUsage(raw)
}

// parseOpenCodeUsage parses the usage payload: the vendor reports *used*
// percent per window, so each becomes its complement.
func parseOpenCodeUsage(raw []byte) (*LiveUsage, error) {
	var root map[string]interface{}
	if json.Unmarshal(raw, &root) != nil {
		return nil, fmt.Errorf("invalid OpenCode usage JSON")
	}
	usage := objectMap(root["usage"])
	if usage == nil {
		return nil, nil
	}
	windows := make([]UsageWindow, 0, 3)
	for _, pair := range [][2]string{
		{"rolling", "5 hours"},
		{"weekly", "Weekly"},
		{"monthly", "Monthly"},
	} {
		entry := objectMap(usage[pair[0]])
		percent := numberValue(entry["percent"])
		if math.IsNaN(percent) {
			continue
		}
		window := UsageWindow{
			Label:            pair[1],
			RemainingPercent: clampPercent(100 - percent),
		}
		if at, err := time.Parse(time.RFC3339, stringValue(entry["resetsAt"])); err == nil {
			ms := float64(at.UnixMilli())
			window.ResetsAtMs = &ms
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return nil, nil
	}
	return &LiveUsage{Windows: windows}, nil
}
