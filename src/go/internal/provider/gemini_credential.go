package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/proxy"
)

// gemini OAuth/API-key egress. Ports the auth half of gemini-proxy/server.ts:
// api-key mode posts straight to generativelanguage.googleapis.com; OAuth mode
// (gemini-cli or antigravity) refreshes a refresh_token grant and posts to the
// Code Assist upstream with an Authorization: Bearer envelope.

const (
	geminiCliOAuthSource   = "gemini-cli"
	antigravityOAuthSource = "antigravity"
)

// OAuth client profiles. Mirrors oauthProfile (server.ts) — constants with
// ANTIGRAVITY_* / GEMINI_OAUTH_* env overrides.
const (
	geminiOAuthClientID       = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOAuthClientSecret   = "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"
	antigravityOAuthClientID  = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthSecret    = "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"
	geminiTokenEndpoint       = "https://oauth2.googleapis.com/token"
	geminiCodeAssistUpstream  = "https://cloudcode-pa.googleapis.com"
	antigravityCodeAssistBase = "https://daily-cloudcode-pa.googleapis.com"
	geminiAPIKeyUpstream      = "https://generativelanguage.googleapis.com"
)

const geminiOAuthRefreshSkew = 60 * time.Second

type geminiOAuthProfile struct {
	clientID     string
	clientSecret string
	ideType      string // GEMINI_CLI | ANTIGRAVITY
	userAgent    string
}

func geminiOAuthProfileFor(source string) geminiOAuthProfile {
	if source == antigravityOAuthSource {
		return geminiOAuthProfile{
			clientID:     envOr("ANTIGRAVITY_OAUTH_CLIENT_ID", antigravityOAuthClientID),
			clientSecret: envOr("ANTIGRAVITY_OAUTH_CLIENT_SECRET", antigravityOAuthSecret),
			ideType:      "ANTIGRAVITY",
			userAgent:    "antigravity/hub/2.3.1 (aidev_client)",
		}
	}
	return geminiOAuthProfile{
		clientID:     envOr("GEMINI_OAUTH_CLIENT_ID", geminiOAuthClientID),
		clientSecret: envOr("GEMINI_OAUTH_CLIENT_SECRET", geminiOAuthClientSecret),
		ideType:      "GEMINI_CLI",
	}
}

func envOr(env, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return fallback
}

// geminiOAuthCredentials ports GeminiOAuthCredentials: the on-disk
// oauth_creds.json shape (expiry_date is a millisecond epoch).
type geminiOAuthCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiryDate   *int64 `json:"expiry_date"`
	Scope        string `json:"scope"`
	ClientID     string `json:"client_id,omitempty"`
}

func geminiExpired(c *geminiOAuthCredentials, now time.Time) bool {
	if c.ExpiryDate == nil {
		return c.AccessToken == ""
	}
	return *c.ExpiryDate < now.Add(geminiOAuthRefreshSkew).UnixMilli()
}

func geminiOAuthCredsPath(snapshotDir, source string) string {
	if source == antigravityOAuthSource {
		if env := os.Getenv("ANTIGRAVITY_OAUTH_FILE"); env != "" {
			return env
		}
		return filepath.Join(snapshotDir, "antigravity_oauth.json")
	}
	return filepath.Join(snapshotDir, "oauth_creds.json")
}

func loadGeminiOAuthCredentials(path, source string) (*geminiOAuthCredentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, anyerr.Newf(
				"No Gemini "+source+" OAuth credentials found at "+path+".",
				"GEMINI_NO_OAUTH")
		}
		return nil, err
	}
	var c geminiOAuthCredentials
	if json.Unmarshal(raw, &c) != nil || (c.RefreshToken == "" && c.AccessToken == "") {
		return nil, anyerr.Newf("Invalid Gemini OAuth credentials at "+path+".", "GEMINI_NO_OAUTH")
	}
	return &c, nil
}

// geminiEnsureAccessToken ports currentOAuthToken + refreshOAuthToken: return a
// valid access token, refreshing the refresh_token grant when expired. gemini-cli
// refreshes under a file lock and writes the rotated token back to oauth_creds.json;
// antigravity refreshes in-process only.
func geminiEnsureAccessToken(snapshotDir, source string) (string, error) {
	path := geminiOAuthCredsPath(snapshotDir, source)
	c, err := loadGeminiOAuthCredentials(path, source)
	if err != nil {
		return "", err
	}
	if c.AccessToken != "" && !geminiExpired(c, time.Now()) {
		return c.AccessToken, nil
	}
	if c.RefreshToken == "" {
		return "", anyerr.Newf(
			"Gemini OAuth access token expired and no refresh_token available.", "GEMINI_NO_OAUTH")
	}
	if source != geminiCliOAuthSource {
		return refreshGeminiOAuth(path, source, c)
	}
	// gemini-cli: serialize the refresh across processes.
	fl, lerr := lock.AcquireFileLock(path+".refresh.lock", 30*time.Second)
	if lerr != nil {
		return "", anyerr.Wrapf(lerr, "acquire Gemini refresh lock", "GEMINI_OAUTH")
	}
	defer fl.Release()
	// A concurrent refresher may have rotated the token while we waited.
	if latest, lerr := loadGeminiOAuthCredentials(path, source); lerr == nil {
		if latest.AccessToken != c.AccessToken || latest.RefreshToken != c.RefreshToken {
			if latest.AccessToken != "" && !geminiExpired(latest, time.Now()) {
				return latest.AccessToken, nil
			}
			c = latest
		}
	}
	return refreshGeminiOAuth(path, source, c)
}

func refreshGeminiOAuth(path, source string, c *geminiOAuthCredentials) (string, error) {
	profile := geminiOAuthProfileFor(source)
	form := url.Values{
		"client_id":     {profile.clientID},
		"client_secret": {profile.clientSecret},
		"refresh_token": {c.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiTokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", anyerr.Wrapf(err, "build Gemini refresh request", "GEMINI_OAUTH")
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", anyerr.Wrapf(err, "Gemini OAuth refresh failed", "GEMINI_OAUTH")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", anyerr.Newf(
			fmt.Sprintf("Gemini OAuth refresh failed (%d): %s", resp.StatusCode, string(body)),
			"GEMINI_OAUTH")
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    *int   `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		msg := tok.ErrorDesc
		if msg == "" {
			msg = tok.Error
		}
		if msg == "" {
			msg = "Gemini OAuth refresh returned no access_token"
		}
		return "", anyerr.Newf(msg, "GEMINI_OAUTH")
	}
	expiresAt := time.Now().Add(time.Duration(derefInt(tok.ExpiresIn, 3600)) * time.Second).UnixMilli()
	updated := *c
	updated.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		updated.RefreshToken = tok.RefreshToken
	}
	if tok.TokenType != "" {
		updated.TokenType = tok.TokenType
	}
	updated.ExpiryDate = &expiresAt
	// gemini-cli persists the rotated session; antigravity is keychain-only.
	if source == geminiCliOAuthSource {
		if werr := writeGeminiOAuthCredentials(path, &updated); werr != nil {
			return "", werr
		}
	}
	return updated.AccessToken, nil
}

// writeGeminiOAuthCredentials preserves the on-disk oauth_creds.json shape and
// only rotates the live fields (ports the undefined-key omission of
// JSON.stringify on the TS raw object).
func writeGeminiOAuthCredentials(path string, c *geminiOAuthCredentials) error {
	var data map[string]json.RawMessage
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &data)
	}
	if data == nil {
		data = map[string]json.RawMessage{}
	}
	if b, err := json.Marshal(c.AccessToken); err == nil {
		data["access_token"] = b
	}
	if b, err := json.Marshal(c.TokenType); err == nil && c.TokenType != "" {
		data["token_type"] = b
	}
	if b, err := json.Marshal(*c.ExpiryDate); err == nil {
		data["expiry_date"] = b
	}
	if c.RefreshToken != "" {
		if b, err := json.Marshal(c.RefreshToken); err == nil {
			data["refresh_token"] = b
		}
	}
	if c.Scope != "" {
		if b, err := json.Marshal(c.Scope); err == nil {
			data["scope"] = b
		}
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

func derefInt(v *int, fallback int) int {
	if v == nil {
		return fallback
	}
	return *v
}

func geminiApiKeyFromSnapshot(snapshotDir string) string {
	if env := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")); env != "" {
		return env
	}
	envFile := snapshotDir
	if !strings.HasSuffix(envFile, ".env") {
		envFile = filepath.Join(snapshotDir, ".env")
	}
	if raw, err := os.ReadFile(envFile); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "export ")
			switch {
			case strings.HasPrefix(line, "GEMINI_API_KEY="):
				return strings.Trim(strings.TrimPrefix(line, "GEMINI_API_KEY="), `"`)
			case strings.HasPrefix(line, "GOOGLE_API_KEY="):
				return strings.Trim(strings.TrimPrefix(line, "GOOGLE_API_KEY="), `"`)
			}
		}
	}
	return ""
}

func geminiOAuthSourceFromSnapshot(snapshotDir string) string {
	// Snapshot/env override wins; otherwise infer from the stored client_id.
	if env := strings.TrimSpace(os.Getenv("GEMINI_OAUTH_SOURCE")); env != "" {
		return env
	}
	if raw, err := os.ReadFile(filepath.Join(snapshotDir, ".env")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "export ")
			if strings.HasPrefix(line, "GEMINI_OAUTH_SOURCE=") {
				return strings.Trim(strings.TrimPrefix(line, "GEMINI_OAUTH_SOURCE="), `"`)
			}
		}
	}
	path := geminiOAuthCredsPath(snapshotDir, geminiCliOAuthSource)
	if raw, err := os.ReadFile(path); err == nil {
		var c struct {
			ClientID string `json:"client_id"`
		}
		if json.Unmarshal(raw, &c) == nil && c.ClientID == antigravityOAuthClientID {
			return antigravityOAuthSource
		}
	}
	return geminiCliOAuthSource
}

func geminiCodeAssistBase(source string) *url.URL {
	var s string
	if env := os.Getenv("GEMINI_CODE_ASSIST_UPSTREAM"); env != "" {
		s = env
	} else if source == antigravityOAuthSource {
		s = antigravityCodeAssistBase
	} else {
		s = geminiCodeAssistUpstream
	}
	u, err := url.Parse(strings.TrimSuffix(s, "/"))
	if err != nil {
		u, _ = url.Parse(geminiCodeAssistUpstream)
	}
	return u
}

// geminiKeyUpstream builds the API-key Upstream from a key already in memory.
// It is the whole of geminiUpstream's key branch, extracted so a gateway
// profile — which has no account snapshot — can reach it without the key ever
// touching disk. See gemini.GatewayUpstreamWithKey for why that matters.
func geminiKeyUpstream(key string) (*proxy.Upstream, error) {
	base, err := url.Parse(geminiAPIKeyUpstream)
	if err != nil {
		return nil, anyerr.Wrapf(err, "parse Gemini upstream base", "PROXY_START_FAILED")
	}
	if env := os.Getenv("GEMINI_API_KEY_UPSTREAM"); env != "" {
		if ob, perr := url.Parse(strings.TrimSuffix(env, "/")); perr == nil {
			base = ob
		}
	}
	return &proxy.Upstream{
		BaseURL:         base,
		Auth:            key,
		AuthHeader:      "x-goog-api-key",
		AuthValuePrefix: "",
		TranslateGemini: true,
		Gemini:          &proxy.GeminiTarget{APIKeyMode: true, APIKeyBase: base},
	}, nil
}

// geminiUpstream builds the shared-proxy Upstream for this snapshot: API-key
// mode posts to generativelanguage.googleapis.com with x-goog-api-key; OAuth
// mode posts through serveGemini's Code Assist envelope with a per-request
// refreshed Bearer token. Both set TranslateGemini + Gemini so the proxy
// dispatches to serveGemini.
func geminiUpstream(snapshotDir string) (*proxy.Upstream, error) {
	if key := geminiApiKeyFromSnapshot(snapshotDir); key != "" {
		return geminiKeyUpstream(key)
	}
	source := geminiOAuthSourceFromSnapshot(snapshotDir)
	path := geminiOAuthCredsPath(snapshotDir, source)
	if _, err := loadGeminiOAuthCredentials(path, source); err != nil {
		return nil, err
	}
	provider := func(ctx context.Context) (string, error) {
		return geminiEnsureAccessToken(snapshotDir, source)
	}
	target := &proxy.GeminiTarget{
		APIKeyMode:     false,
		CodeAssistBase: geminiCodeAssistBase(source),
	}
	// Auto-fallback: when the snapshot also carries the other identity, retry
	// once through it on 401/403/404 (ports callGeminiWithAutoFallback's
	// gemini-cli ↔ antigravity failover).
	if alt := geminiAltRouteFromSnapshot(snapshotDir, source); alt != nil {
		target.AltRoute = alt
	}
	return &proxy.Upstream{
		BaseURL:         geminiCodeAssistBase(source),
		AuthHeader:      "Authorization",
		AuthValuePrefix: "Bearer ",
		AuthProvider:    provider,
		TranslateGemini: true,
		Gemini:          target,
	}, nil
}

// geminiAltRouteFromSnapshot returns an AltRoute for the OTHER OAuth identity
// when the snapshot holds usable credentials for it, enabling single-account
// auto auth fallback. Returns nil when no alternate is present.
func geminiAltRouteFromSnapshot(snapshotDir, source string) *proxy.GeminiAltRoute {
	altSource := antigravityOAuthSource
	if source == antigravityOAuthSource {
		altSource = geminiCliOAuthSource
	}
	altPath := geminiOAuthCredsPath(snapshotDir, altSource)
	if _, err := loadGeminiOAuthCredentials(altPath, altSource); err != nil {
		return nil
	}
	return &proxy.GeminiAltRoute{
		CodeAssistBase: geminiCodeAssistBase(altSource),
		AuthProvider: func(ctx context.Context) (string, error) {
			return geminiEnsureAccessToken(snapshotDir, altSource)
		},
	}
}
