package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// Cline (ClinePass) subscription account source.
//
// Cline sells ClinePass ($9.99/mo, curated open-weight coding models) alongside
// usage billing, both behind one WorkOS OAuth login whose tokens live in the
// app's own providers.json. AnyPick borrows that login the same way it borrows
// WorkBuddy's and Z.ai's: it runs the vendor's own device flow itself, owns the
// resulting tokens end to end, and proxies chat through the vendor gateway.
//
// Provenance is deliberately "community": the API below is recovered from the
// open-source Cline CLI/SDK, not published by Cline, and ClinePass terms are
// written around Cline's own surfaces. Cross-harness use is best-effort.
//
// Two stores are involved and only one is authoritative:
//
//	~/.cline-anypick/auth.json   AnyPick staging (written by Login)
//	~/.anypick/providers/cline/accounts/<name>/…   the snapshot
//
// ~/.cline/data/settings/providers.json (Cline's own store) is read for
// DetectLive/Backup fallback only and never written: its ProviderSettingsManager
// owns atomic writes there, and fighting it is how managed config gets lost.
const (
	clineAPIBaseDefault = "https://api.cline.bot"
	clineWorkOSBase     = "https://api.workos.com"

	// clineWorkOSClientIDDefault is Cline's public OAuth client for
	// production. WorkOS device flow needs no client secret by design (same
	// posture as the Tencent device flow workbuddy runs), so embedding it is
	// the documented pattern, not a leaked secret. Override for tests.
	clineWorkOSClientIDDefault = "client_01K3A541FN8TA3EPPHTD2325AR"

	clineDevicePollInterval = 5 * time.Second
	clineDevicePollTimeout  = 5 * time.Minute

	// clineRefreshBufferMs mirrors Cline's own 5-minute refresh buffer:
	// a token inside the buffer counts as expired.
	clineRefreshBufferMs = 5 * 60 * 1000
	// clineRefreshGraceMs mirrors Cline's 30s grace: on transient refresh
	// failures a still-valid token keeps serving instead of logging out
	// every process on the machine.
	clineRefreshGraceMs = 30 * 1000
)

// clineAPIBase returns the Cline API root. CLINE_API_BASE_URL overrides the
// whole base and doubles as the test hook (httptest stand-in).
func clineAPIBase() string {
	if base := strings.TrimSpace(os.Getenv("CLINE_API_BASE_URL")); base != "" {
		return strings.TrimSuffix(base, "/")
	}
	return clineAPIBaseDefault
}

func clineWorkOSClientID() string {
	if id := strings.TrimSpace(os.Getenv("CLINE_WORKOS_CLIENT_ID")); id != "" {
		return id
	}
	return clineWorkOSClientIDDefault
}

// clineAuthRoot is the API root auth calls build on. It honors
// CLINE_API_BASE_URL by trimming the upstream /api/v1 suffix the override
// carries, so tests point every Cline surface at one stand-in.
func clineAuthRoot() string {
	if base := strings.TrimSpace(os.Getenv("CLINE_API_BASE_URL")); base != "" {
		return strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/api/v1")
	}
	return clineAPIBase()
}

// clineCredentials is the live token set: registered Cline access + WorkOS
// refresh, with expiry in epoch millis (providers.json convention).
type clineCredentials struct {
	Access    string
	Refresh   string
	ExpiresMs int64
	AccountID string
	Email     string
}

// ErrClineReauth reports a definitively rejected refresh token: signing in
// again is the only repair. Transient failures are plain errors so callers
// can keep serving a still-valid token (Cline's own contract).
var ErrClineReauth = &clineReauthError{}

type clineReauthError struct{}

func (e *clineReauthError) Error() string { return "Cline refresh token was rejected; sign in again" }

// clineHTTPTimeout bounds one request. Var, not const: tests shrink it to
// prove stalled connections fail fast.
var clineHTTPTimeout = 30 * time.Second

func clineHTTPClient() *http.Client {
	return &http.Client{Timeout: clineHTTPTimeout}
}

type clineEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

type clineTokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresAt    string `json:"expiresAt"`
	UserInfo     struct {
		Email     string   `json:"email"`
		Name      string   `json:"name"`
		ClineUser string   `json:"clineUserId"`
		Accounts  []string `json:"accounts"`
	} `json:"userInfo"`
}

// clineDeviceLogin runs WorkOS device auth end to end and registers the
// tokens with the Cline API, returning registered Cline credentials. It
// returns raw tokens; persisting them is the caller's job.
func clineDeviceLogin(ctx context.Context) (*clineCredentials, error) {
	clientID := clineWorkOSClientID()
	form := url.Values{"client_id": {clientID}}
	req, err := http.NewRequest(http.MethodPost,
		clineWorkOSBase+"/user_management/authorize/device",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, anyerr.Wrapf(err, "build the Cline sign-in request", "PROVIDER_LOGIN_FAILED")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	if err := clineWorkOS(ctx, req, &device); err != nil {
		return nil, err
	}
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		return nil, anyerr.Newf("Cline sign-in did not return a device code.",
			"PROVIDER_LOGIN_FAILED")
	}
	uri := device.VerificationURIComplete
	if uri == "" {
		uri = device.VerificationURI
	}
	// The URL + code are one-time and only printed when the browser cannot
	// be opened — stdout stays clean for --json.
	if err := openClineBrowserURL(uri); err != nil {
		fmt.Fprintln(os.Stderr, "Could not open a browser ("+err.Error()+"). Open this URL and enter the code "+device.UserCode+":")
		fmt.Fprintln(os.Stderr, "  "+uri)
	} else {
		fmt.Fprintln(os.Stderr, "Enter this code in your browser to finish signing in: "+device.UserCode)
	}
	workosAccess, workosRefresh, err := clinePollWorkOS(ctx, clientID, device.DeviceCode, device.ExpiresIn, device.Interval)
	if err != nil {
		return nil, err
	}
	return clineRegister(ctx, workosAccess, workosRefresh)
}

func clineWorkOS(ctx context.Context, req *http.Request, out interface{}) error {
	resp, err := clineHTTPClient().Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return anyerr.Newf("Cline sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		}
		return anyerr.Wrapf(err, "reach the Cline sign-in service", "PROVIDER_LOGIN_FAILED")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return anyerr.Wrapf(err, "read the Cline sign-in response", "PROVIDER_LOGIN_FAILED")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return anyerr.Newf(
			fmt.Sprintf("Cline sign-in service returned HTTP %d.", resp.StatusCode),
			"PROVIDER_LOGIN_FAILED")
	}
	if json.Unmarshal(body, out) != nil {
		return anyerr.Newf("Cline sign-in service returned an unreadable response.",
			"PROVIDER_LOGIN_FAILED")
	}
	return nil
}

func clinePollWorkOS(ctx context.Context, clientID, deviceCode string, expiresIn, intervalSec int64) (string, string, error) {
	if expiresIn <= 0 {
		expiresIn = int64((clineDevicePollTimeout) / time.Second)
	}
	interval := time.Duration(intervalSec) * time.Second
	if interval < time.Second {
		interval = clineDevicePollInterval
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	for {
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
			"client_id":   {clientID},
		}
		req, err := http.NewRequest(http.MethodPost,
			clineWorkOSBase+"/user_management/authenticate",
			strings.NewReader(form.Encode()))
		if err != nil {
			return "", "", anyerr.Wrapf(err, "build the Cline token request", "PROVIDER_LOGIN_FAILED")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		var payload struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Error        string `json:"error"`
			ErrorDesc    string `json:"error_description"`
		}
		if err := clineWorkOS(ctx, req, &payload); err != nil {
			return "", "", err
		}
		if payload.AccessToken != "" {
			if payload.RefreshToken == "" {
				return "", "", anyerr.Newf("Cline sign-in finished without a refresh token.",
					"PROVIDER_LOGIN_FAILED")
			}
			return payload.AccessToken, payload.RefreshToken, nil
		}
		switch payload.Error {
		case "authorization_pending":
		case "slow_down":
			interval += time.Second
		case "access_denied", "expired_token", "invalid_grant":
			return "", "", anyerr.Newf("Cline sign-in was not approved ("+payload.Error+").",
				"PROVIDER_LOGIN_FAILED")
		default:
			if payload.Error != "" {
				return "", "", anyerr.Newf("Cline sign-in failed ("+payload.Error+").",
					"PROVIDER_LOGIN_FAILED")
			}
		}
		if !time.Now().Before(deadline) {
			return "", "", anyerr.Newf("Cline sign-in timed out after five minutes.",
				"PROVIDER_LOGIN_TIMEOUT",
				anyerr.WithSuggestions("Try Sign In again and approve it in the browser within five minutes."))
		}
		select {
		case <-ctx.Done():
			return "", "", anyerr.Newf("Cline sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		case <-time.After(interval):
		}
	}
}

// clineRegister exchanges WorkOS tokens for registered Cline credentials.
func clineRegister(ctx context.Context, workosAccess, workosRefresh string) (*clineCredentials, error) {
	body, _ := json.Marshal(map[string]string{
		"accessToken": workosAccess, "refreshToken": workosRefresh,
	})
	req, err := http.NewRequest(http.MethodPost, clineAuthRoot()+"/api/v1/auth/register",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, anyerr.Wrapf(err, "build the Cline token request", "PROVIDER_LOGIN_FAILED")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := clineHTTPClient().Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil, anyerr.Newf("Cline sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		}
		return nil, anyerr.Wrapf(err, "register the Cline sign-in", "PROVIDER_LOGIN_FAILED")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, anyerr.Wrapf(err, "read the Cline sign-in response", "PROVIDER_LOGIN_FAILED")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, anyerr.Newf(
			fmt.Sprintf("Cline token registration failed (HTTP %d).", resp.StatusCode),
			"PROVIDER_LOGIN_FAILED")
	}
	var env clineEnvelope
	var data clineTokenData
	if json.Unmarshal(raw, &env) != nil || !env.Success ||
		json.Unmarshal(env.Data, &data) != nil || data.AccessToken == "" {
		return nil, anyerr.Newf("Cline sign-in finished without an access token.",
			"PROVIDER_LOGIN_FAILED")
	}
	if data.RefreshToken == "" {
		return nil, anyerr.Newf("Cline sign-in finished without a refresh token.",
			"PROVIDER_LOGIN_FAILED")
	}
	return &clineCredentials{
		Access: data.AccessToken, Refresh: data.RefreshToken,
		ExpiresMs: clineParseExpiry(data.ExpiresAt),
		AccountID: data.UserInfo.ClineUser, Email: data.UserInfo.Email,
	}, nil
}

// clineRefreshTokens exchanges a refresh token for a live set. It returns
// ErrClineReauth only when the server definitively rejects the grant
// (re-auth required); transport failures are plain errors so callers can
// keep serving a still-valid token, mirroring Cline's own contract.
func clineRefreshTokens(ctx context.Context, refresh string) (*clineCredentials, error) {
	body, _ := json.Marshal(map[string]string{
		"refreshToken": refresh, "grantType": "refresh_token",
	})
	req, err := http.NewRequest(http.MethodPost, clineAuthRoot()+"/api/v1/auth/refresh",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, anyerr.Wrapf(err, "build the Cline token refresh", "PROVIDER_LOGIN_FAILED")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := clineHTTPClient().Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {
		return nil, ErrClineReauth
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, anyerr.Newf(
			fmt.Sprintf("Cline token refresh failed (HTTP %d).", resp.StatusCode),
			"PROVIDER_LOGIN_FAILED")
	}
	var env clineEnvelope
	var data clineTokenData
	if json.Unmarshal(raw, &env) != nil || !env.Success ||
		json.Unmarshal(env.Data, &data) != nil || data.AccessToken == "" {
		return nil, anyerr.Newf("Cline token refresh returned no access token.",
			"PROVIDER_LOGIN_FAILED")
	}
	return &clineCredentials{
		Access: data.AccessToken, Refresh: firstNonEmpty(data.RefreshToken, refresh),
		ExpiresMs: clineParseExpiry(data.ExpiresAt),
		AccountID: data.UserInfo.ClineUser, Email: data.UserInfo.Email,
	}, nil
}

// clineBearerToken formats the gateway bearer the way Cline itself does:
// the "workos:" namespace prefix is ensured present (added when missing),
// never stripped.
func clineBearerToken(access string) string {
	trimmed := strings.TrimSpace(access)
	if strings.HasPrefix(strings.ToLower(trimmed), "workos:") {
		return trimmed
	}
	return "workos:" + trimmed
}

// clineValidCredentials returns usable credentials, refreshing inside the
// buffer. It returns ErrClineReauth only on definitive rejection; on
// transient failure with a still-valid token it returns the stale set,
// exactly like getValidClineCredentials.
func clineValidCredentials(ctx context.Context, cur *clineCredentials) (*clineCredentials, error) {
	if cur == nil || cur.Access == "" {
		return nil, ErrClineReauth
	}
	if cur.ExpiresMs-clineNowMs() > clineRefreshBufferMs {
		return cur, nil
	}
	refreshed, err := clineRefreshTokens(ctx, cur.Refresh)
	if err == nil {
		return refreshed, nil
	}
	if err == ErrClineReauth {
		return nil, err
	}
	if cur.ExpiresMs-clineNowMs() > clineRefreshGraceMs {
		return cur, nil
	}
	return nil, err
}

func clineParseExpiry(iso string) int64 {
	if iso == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.UnixMilli()
	}
	return 0
}

// clineNowMs is millisecond now, split for tests.
func clineNowMs() int64 { return time.Now().UnixMilli() }

// openClineBrowserURL hands a sign-in URL to the desktop browser. Only https
// URLs are accepted: the value comes from a remote service and is passed to
// a platform launcher.
func openClineBrowserURL(raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("refusing to open a non-https URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw) // #nosec G204 — https URL validated above
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw) // #nosec G204
	default:
		cmd = exec.Command("xdg-open", raw) // #nosec G204
	}
	return cmd.Start()
}
