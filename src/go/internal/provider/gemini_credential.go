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
	"strconv"
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
	geminiOAuthClientID      = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOAuthClientSecret  = "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"
	antigravityOAuthClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthSecret   = "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"
	// geminiTokenEndpointDefault is the production OAuth token URL. Tests
	// point GEMINI_TOKEN_ENDPOINT at a loopback httptest server instead; see
	// oauthTokenEndpoint, which refuses non-loopback overrides.
	geminiTokenEndpointDefault = "https://oauth2.googleapis.com/token"
	geminiCodeAssistUpstream   = "https://cloudcode-pa.googleapis.com"
	antigravityCodeAssistBase  = "https://daily-cloudcode-pa.googleapis.com"
	geminiAPIKeyUpstream       = "https://generativelanguage.googleapis.com"
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

// geminiTokenEndpoint returns the OAuth token URL. GEMINI_TOKEN_ENDPOINT
// overrides it for tests and debugging; production uses Google's endpoint.
func envOr(env, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return fallback
}

// On-disk OAuth file names, in probe order. gemini-cli writes a single flat
// oauth_creds.json. Antigravity does not: depending on the build it leaves
// either antigravity_oauth.json or the standalone Jetski token file in
// ~/.gemini, and the two names are interchangeable in content. Probing both
// is what lets an Antigravity login survive a backup without a rename.
var geminiCliCredentialFiles = []string{"oauth_creds.json"}

var antigravityCredentialFiles = []string{"antigravity_oauth.json", "jetski-standalone-oauth-token"}

// geminiCredentialFilesFor returns the candidate names for a source.
func geminiCredentialFilesFor(source string) []string {
	if source == antigravityOAuthSource {
		return antigravityCredentialFiles
	}
	return geminiCliCredentialFiles
}

// geminiAllCredentialFiles is the live-login and backup set: every name a
// ~/.gemini login may use, gemini-cli first so an explicit source still wins.
func geminiAllCredentialFiles() []string {
	out := make([]string, 0, len(geminiCliCredentialFiles)+len(antigravityCredentialFiles))
	out = append(out, geminiCliCredentialFiles...)
	out = append(out, antigravityCredentialFiles...)
	return out
}

// geminiOAuthCredentials ports GeminiOAuthCredentials: the flat on-disk
// oauth_creds.json shape (expiry_date is a millisecond epoch).
//
// Antigravity stores the same fields one level down, under `token`, with an
// RFC3339 `expiry` instead of a millisecond epoch, plus `auth_method` and
// `email`. `nested` records that envelope when the file was read in that
// shape so a rewrite keeps the layout the owning app expects rather than
// silently converting it into a gemini-cli file.
type geminiOAuthCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiryDate   *int64 `json:"expiry_date"`
	Scope        string `json:"scope"`
	ClientID     string `json:"client_id,omitempty"`

	nested *geminiAntigravityFile
}

// geminiAntigravityFile is the Antigravity / Jetski on-disk envelope.
type geminiAntigravityFile struct {
	Token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Expiry       string `json:"expiry"`
	} `json:"token"`
	AuthMethod string `json:"auth_method"`
	Email      string `json:"email"`
}

func geminiExpired(c *geminiOAuthCredentials, now time.Time) bool {
	if c.ExpiryDate == nil {
		return c.AccessToken == ""
	}
	return *c.ExpiryDate < now.Add(geminiOAuthRefreshSkew).UnixMilli()
}

// geminiOAuthCredsPath returns the credential file for a source: the first
// candidate name that exists on disk, else the canonical name so an error
// points at the file the user would expect to see.
func geminiOAuthCredsPath(snapshotDir, source string) string {
	if source == antigravityOAuthSource {
		if env := os.Getenv("ANTIGRAVITY_OAUTH_FILE"); env != "" {
			return env
		}
	}
	names := geminiCredentialFilesFor(source)
	for _, name := range names {
		path := filepath.Join(snapshotDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(snapshotDir, names[0])
}

// geminiHasCredentials reports whether a source has any credential file.
// Existence only: parsing belongs to the loader, and a corrupt file must read
// as "present but broken", not as "absent".
func geminiHasCredentials(dir, source string) bool {
	for _, name := range geminiCredentialFilesFor(source) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
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
	c, ok := parseGeminiOAuthCredentials(raw)
	if !ok {
		return nil, anyerr.Newf("Invalid Gemini OAuth credentials at "+path+".", "GEMINI_NO_OAUTH")
	}
	return c, nil
}

// parseGeminiOAuthCredentials reads either on-disk shape: the flat
// gemini-cli file, or the nested Antigravity/Jetski envelope.
func parseGeminiOAuthCredentials(raw []byte) (*geminiOAuthCredentials, bool) {
	var c geminiOAuthCredentials
	if json.Unmarshal(raw, &c) != nil {
		return nil, false
	}
	var env geminiAntigravityFile
	if json.Unmarshal(raw, &env) == nil &&
		(env.Token.AccessToken != "" || env.Token.RefreshToken != "") {
		c.AccessToken = env.Token.AccessToken
		c.RefreshToken = env.Token.RefreshToken
		if env.Token.TokenType != "" {
			c.TokenType = env.Token.TokenType
		}
		if c.ExpiryDate == nil {
			c.ExpiryDate = parseGeminiExpiry(env.Token.Expiry)
		}
		nested := env
		c.nested = &nested
	}
	if c.RefreshToken == "" && c.AccessToken == "" {
		return nil, false
	}
	return &c, true
}

// geminiEmailFromCredentials extracts a display identity from either
// credential shape: Antigravity stores the address in clear text at the
// envelope root, gemini-cli hides it inside the id_token.
func geminiEmailFromCredentials(raw []byte) string {
	var env geminiAntigravityFile
	if json.Unmarshal(raw, &env) == nil && strings.Contains(env.Email, "@") {
		return env.Email
	}
	var flat struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(raw, &flat) == nil && flat.IDToken != "" {
		return jwtEmail(flat.IDToken)
	}
	return ""
}

// parseGeminiExpiry accepts the expiry forms both writers emit: an RFC3339
// timestamp (Antigravity) and a millisecond epoch (gemini-cli, when a build
// inlines it into the nested file). nil means "unknown", which reads as
// expired unless an access token is present.
func parseGeminiExpiry(value string) *int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			ms := ts.UnixMilli()
			return &ms
		}
	}
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		// Epoch seconds are ~1e9 and milliseconds ~1e12; anything smaller
		// than 2000-01-01 in ms must be seconds.
		if ms < 946684800000 {
			ms *= 1000
		}
		return &ms
	}
	return nil
}

// geminiEnsureAccessToken ports currentOAuthToken + refreshOAuthToken: return a
// valid access token, refreshing the refresh_token grant when expired. Both
// sources refresh under a file lock and write the rotated token back: the
// credential is a file we own (the snapshot, or ANTIGRAVITY_OAUTH_FILE), and
// skipping the write would turn every proxied request into an OAuth
// round-trip — the AuthProvider runs per request, not per process.
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
	endpoint, err := oauthTokenEndpoint("GEMINI_TOKEN_ENDPOINT", geminiTokenEndpointDefault)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
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
	// Bound the OAuth body: the endpoint is outside our trust boundary and
	// its payload must never flow verbatim into errors, logs, or clients.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", anyerr.Newf(
			fmt.Sprintf("Gemini OAuth refresh failed (HTTP %d)", resp.StatusCode),
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
	if werr := writeGeminiOAuthCredentials(path, &updated); werr != nil {
		return "", werr
	}
	return updated.AccessToken, nil
}

// writeGeminiOAuthCredentials preserves the on-disk shape and only rotates the
// live fields (ports the undefined-key omission of JSON.stringify on the TS raw
// object). A file that was read as the nested Antigravity envelope is written
// back as one: converting it to the flat shape would leave the owning app
// unable to read its own login.
func writeGeminiOAuthCredentials(path string, c *geminiOAuthCredentials) error {
	if c.nested != nil {
		return writeGeminiNestedCredentials(path, c)
	}
	return writeGeminiFlatCredentials(path, c)
}

// writeGeminiNestedCredentials rewrites {"token":{…}} in place, keeping every
// key this build does not own (auth_method, email, and anything a future
// version adds).
func writeGeminiNestedCredentials(path string, c *geminiOAuthCredentials) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		data = map[string]json.RawMessage{}
	}
	var token map[string]json.RawMessage
	if existing, ok := data["token"]; ok {
		_ = json.Unmarshal(existing, &token)
	}
	if token == nil {
		token = map[string]json.RawMessage{}
	}
	token["access_token"] = mustJSON(c.AccessToken)
	if c.RefreshToken != "" {
		token["refresh_token"] = mustJSON(c.RefreshToken)
	}
	if c.TokenType != "" {
		token["token_type"] = mustJSON(c.TokenType)
	}
	if c.ExpiryDate != nil {
		token["expiry"] = mustJSON(time.UnixMilli(*c.ExpiryDate).Format(time.RFC3339Nano))
	}
	body, err := json.Marshal(token)
	if err != nil {
		return err
	}
	data["token"] = body
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

func writeGeminiFlatCredentials(path string, c *geminiOAuthCredentials) error {
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
	// No gemini-cli credential on disk: fall to whichever source actually has
	// a file. Without this an Antigravity-only snapshot is classified as
	// gemini-cli and `proxy start` dies looking for the file that is not
	// there, while the usable credential sits next to it unread.
	if geminiHasCredentials(snapshotDir, geminiCliOAuthSource) {
		return geminiCliOAuthSource
	}
	if geminiHasCredentials(snapshotDir, antigravityOAuthSource) {
		return antigravityOAuthSource
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
	profile := geminiOAuthProfileFor(source)
	target := &proxy.GeminiTarget{
		APIKeyMode:     false,
		CodeAssistBase: geminiCodeAssistBase(source),
		IDEType:        profile.ideType,
		UserAgent:      profile.userAgent,
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
	altProfile := geminiOAuthProfileFor(altSource)
	return &proxy.GeminiAltRoute{
		CodeAssistBase: geminiCodeAssistBase(altSource),
		IDEType:        altProfile.ideType,
		UserAgent:      altProfile.userAgent,
		AuthProvider: func(ctx context.Context) (string, error) {
			return geminiEnsureAccessToken(snapshotDir, altSource)
		},
	}
}
