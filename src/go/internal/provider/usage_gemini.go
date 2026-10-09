package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/proxy"
)

// geminiUsageTimeout bounds each Code Assist call. Quota is best-effort: a
// missing or expired live token, or any vendor error, means "no figure",
// never a failed snapshot.
const geminiUsageTimeout = 5 * time.Second

// LiveUsage reads the current Code Assist quota using whichever live OAuth
// login is valid. It deliberately never refreshes or writes a credential —
// quota polling runs every 30s and must not rotate a login the owning app is
// using.
func (p *gemini) LiveUsage(ctx context.Context) (*LiveUsage, error) {
	token, source := p.liveUsageAccessToken()
	if token == "" {
		return nil, nil
	}
	profile := geminiOAuthProfileFor(source)
	base, err := geminiCodeAssistBase(source)
	if err != nil {
		return nil, err
	}
	usage, err := fetchGeminiCodeAssistUsage(
		ctx, base.String(), token, profile.ideType, profile.userAgent)
	if err != nil {
		return nil, err
	}
	return usage, nil
}

// liveUsageAccessToken returns a currently valid live access token and which
// OAuth identity owns it, or empty strings. The keychain is excluded on
// purpose: a routine status poll must never raise a credentials prompt.
func (p *gemini) liveUsageAccessToken() (string, string) {
	// Follow DetectLive's precedence: the IDE login is authoritative, and a
	// gemini-cli file is the live login only when Antigravity has none.
	// Reading the other source's token would report one account's quota under
	// the current account's name.
	if payload := p.authoritativeAntigravityCredential(); payload != nil {
		c := antigravityUsageCredentials(payload)
		if c == nil || geminiExpired(c, time.Now()) {
			return "", ""
		}
		return c.AccessToken, antigravityOAuthSource
	}
	if p.oauthCredFileFor(geminiCliOAuthSource) == "" {
		return "", ""
	}
	c, err := loadGeminiOAuthCredentials(
		p.livePath(geminiCliCredentialFiles[0]), geminiCliOAuthSource)
	if err != nil || c == nil || c.AccessToken == "" || geminiExpired(c, time.Now()) {
		return "", ""
	}
	return c.AccessToken, geminiCliOAuthSource
}

// antigravityUsageCredentials adapts an Antigravity state/mirror payload to
// the shared OAuth credential shape so validity is judged by one rule.
func antigravityUsageCredentials(payload *geminiAntigravityFile) *geminiOAuthCredentials {
	if payload == nil || payload.Token.AccessToken == "" {
		return nil
	}
	c := &geminiOAuthCredentials{
		AccessToken:  payload.Token.AccessToken,
		RefreshToken: payload.Token.RefreshToken,
		TokenType:    payload.Token.TokenType,
	}
	if expiry := parseGeminiExpiry(payload.Token.Expiry); expiry != nil {
		c.ExpiryDate = expiry
	}
	return c
}

// fetchGeminiCodeAssistUsage resolves the account's Code Assist project and
// asks for its quota. As in the reference implementation, loadCodeAssist can
// answer without a project for consumer accounts, and retrieveUserQuota is
// still the quota read for them.
func fetchGeminiCodeAssistUsage(ctx context.Context, base, token, ideType, userAgent string) (*LiveUsage, error) {
	base = strings.TrimSuffix(base, "/")
	project, err := geminiLoadCodeAssistProject(ctx, base, token, ideType, userAgent)
	if err != nil {
		return nil, err
	}
	raw, err := geminiCodeAssistPost(
		ctx, base+"/v1internal:retrieveUserQuota", token, userAgent,
		map[string]string{"project": project})
	if err != nil {
		return nil, err
	}
	return parseGeminiQuota(raw)
}

func geminiLoadCodeAssistProject(ctx context.Context, base, token, ideType, userAgent string) (string, error) {
	raw, err := geminiCodeAssistPost(ctx, base+"/v1internal:loadCodeAssist", token, userAgent,
		map[string]interface{}{"metadata": proxy.GeminiCodeAssistMetadata(ideType)})
	if err != nil {
		return "", err
	}
	var parsed struct {
		CloudaicompanionProject string `json:"cloudaicompanionProject"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return "", nil
	}
	return parsed.CloudaicompanionProject, nil
}

func geminiCodeAssistPost(ctx context.Context, endpoint, token, userAgent string, payload interface{}) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	client := &http.Client{Timeout: geminiUsageTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A vendor error body can echo request material; only the status
		// crosses into an error.
		return nil, fmt.Errorf("code assist HTTP %d", resp.StatusCode)
	}
	return raw, nil
}

type geminiQuotaBucket struct {
	ModelID string `json:"modelId"`
	// A vendor bucket can carry a non-numeric fraction; the payload is
	// untrusted input and one malformed bucket must not fail the read.
	RemainingFraction interface{} `json:"remainingFraction"`
	ResetTime         string      `json:"resetTime"`
}

// parseGeminiQuota converts retrieveUserQuota buckets into usage windows.
// Buckets that share a fraction and reset instant collapse into one window
// labelled by model family, so 40 model ids do not become 40 rows.
func parseGeminiQuota(raw []byte) (*LiveUsage, error) {
	var body struct {
		Buckets []geminiQuotaBucket `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	windows := make([]UsageWindow, 0, len(body.Buckets))
	for _, bucket := range body.Buckets {
		model := strings.TrimPrefix(strings.TrimSpace(bucket.ModelID), "models/")
		fraction := numberValue(bucket.RemainingFraction)
		if model == "" || math.IsNaN(fraction) || math.IsInf(fraction, 0) {
			continue
		}
		fraction = math.Max(0, math.Min(1, fraction))
		window := UsageWindow{
			Label:            model,
			RemainingPercent: int(math.Round(fraction * 100)),
		}
		if reset, err := time.Parse(time.RFC3339, strings.TrimSpace(bucket.ResetTime)); err == nil {
			ms := float64(reset.UnixMilli())
			window.ResetsAtMs = &ms
		}
		windows = append(windows, window)
	}
	windows = groupGeminiQuotaWindows(windows)
	if len(windows) == 0 {
		return nil, nil
	}
	return &LiveUsage{Windows: windows}, nil
}

func groupGeminiQuotaWindows(windows []UsageWindow) []UsageWindow {
	order := make([]string, 0, len(windows))
	groups := map[string][]UsageWindow{}
	for _, window := range windows {
		reset := "none"
		if window.ResetsAtMs != nil {
			reset = strconv.FormatFloat(*window.ResetsAtMs, 'f', -1, 64)
		}
		key := fmt.Sprintf("%d:%s", window.RemainingPercent, reset)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], window)
	}
	out := make([]UsageWindow, 0, len(order))
	for _, key := range order {
		group := groups[key]
		first := group[0]
		if len(group) == 1 {
			out = append(out, first)
			continue
		}
		families := make([]string, 0, len(group))
		seen := map[string]bool{}
		for _, window := range group {
			family := geminiModelFamily(window.Label)
			if !seen[family] {
				seen[family] = true
				families = append(families, family)
			}
		}
		first.Label = fmt.Sprintf("%s (%d)", strings.Join(families, " + "), len(group))
		out = append(out, first)
	}
	return out
}

// geminiModelFamily names a quota bucket's model family the way the reference
// implementation did: a known family prefix, else the vendor's first id word.
func geminiModelFamily(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "gemini"):
		return "Gemini"
	case strings.HasPrefix(lower, "claude"):
		return "Claude"
	case strings.HasPrefix(lower, "gpt"):
		return "GPT"
	case strings.HasPrefix(lower, "chat"):
		return "Chat"
	case strings.HasPrefix(lower, "tab"):
		return "Tab"
	}
	if idx := strings.IndexAny(lower, "-_"); idx > 0 {
		return lower[:idx]
	}
	if lower == "" {
		return "Other"
	}
	return lower
}
