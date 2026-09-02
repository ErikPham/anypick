package provider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// workbuddyUserAgent mirrors the User-Agent the official CodeBuddy CLI sends.
// The auth endpoints below are not a published API — they were recovered from
// that CLI — so identifying ourselves as a CLI keeps requests on the path
// Tencent actually serves.
const workbuddyUserAgent = "CLI/2.63.2 CodeBuddy/2.63.2"

const (
	workbuddyStatePath = "/v2/plugin/auth/state"
	workbuddyTokenPath = "/v2/plugin/auth/token"

	workbuddyPollInterval = 5 * time.Second
	workbuddyPollTimeout  = 5 * time.Minute

	// workbuddyCodePending is what the token endpoint answers until the user
	// finishes approving in the browser. Zero means the token is ready.
	workbuddyCodePending = 11217
)

// workbuddySite is one Tencent deployment of the CodeBuddy/WorkBuddy auth
// service. Both share the device-flow contract; only the host and domain differ.
type workbuddySite struct {
	MethodID    string
	Label       string
	Description string
	Base        string
	Domain      string
	Preferred   bool
}

// workbuddySites is the ordered sign-in choice list. The methods these produce
// are marked "community", not "official": the flow is not published by Tencent
// and can change without notice.
var workbuddySites = []workbuddySite{
	{
		MethodID:    "workbuddy-international",
		Label:       "WorkBuddy international",
		Description: "Browser sign-in at www.codebuddy.ai",
		Base:        "https://www.codebuddy.ai",
		Domain:      "www.codebuddy.ai",
		Preferred:   true,
	},
	{
		MethodID:    "codebuddy-china",
		Label:       "CodeBuddy China",
		Description: "Browser sign-in at copilot.tencent.com",
		Base:        "https://copilot.tencent.com",
		Domain:      "www.codebuddy.cn",
	},
}

func workbuddySiteByMethod(methodID string) (workbuddySite, bool) {
	for _, site := range workbuddySites {
		if site.MethodID == methodID {
			return site, true
		}
	}
	return workbuddySite{}, false
}

type workbuddyEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type workbuddyStateData struct {
	State   string `json:"state"`
	AuthURL string `json:"authUrl"`
}

type workbuddyTokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
	Domain       string `json:"domain"`
}

// workbuddyDeviceLogin runs the sign-in end to end: ask the service for a
// state, open the browser, then poll until the user approves. It returns the
// raw token; persisting it is the caller's job.
func workbuddyDeviceLogin(ctx context.Context, site workbuddySite) (*workbuddyTokenData, error) {
	state, authURL, err := workbuddyStartLogin(ctx, site)
	if err != nil {
		return nil, err
	}
	// The URL is a one-time authorisation link, so it is only printed when the
	// browser cannot be opened — stdout stays clean for --json and the URL
	// stays out of logs in the normal case.
	if err := openBrowserURL(authURL); err != nil {
		fmt.Fprintln(os.Stderr, "Could not open a browser ("+err.Error()+"). Open this URL to finish signing in:")
		fmt.Fprintln(os.Stderr, "  "+authURL)
	} else {
		fmt.Fprintln(os.Stderr, "Finish signing in in your browser, then wait for AnyPick to pick up the token.")
	}
	return workbuddyAwaitToken(ctx, site, state)
}

func workbuddyStartLogin(ctx context.Context, site workbuddySite) (string, string, error) {
	req, err := http.NewRequest(http.MethodPost,
		site.Base+workbuddyStatePath+"?platform=CLI", strings.NewReader("{}"))
	if err != nil {
		return "", "", anyerr.Wrapf(err, "build the CodeBuddy sign-in request", "PROVIDER_LOGIN_FAILED")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", site.Domain)
	req.Header.Set("X-Product", "SaaS")
	// This first call is unauthenticated; these headers tell the gateway to
	// skip the identity checks it applies to signed requests.
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-User-Id", "true")
	req.Header.Set("X-Enterprise-Id", "true")
	req.Header.Set("X-Department-Info", "true")

	env, err := workbuddyFetch(ctx, req)
	if err != nil {
		return "", "", err
	}
	if env.Code != 0 {
		return "", "", anyerr.Newf(workbuddyServiceMessage(env), "PROVIDER_LOGIN_FAILED")
	}
	var data workbuddyStateData
	if json.Unmarshal(env.Data, &data) != nil || data.State == "" || data.AuthURL == "" {
		return "", "", anyerr.Newf(
			"CodeBuddy sign-in did not return a login URL.",
			"PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions("Try Sign In again; if it keeps failing the service may have changed."))
	}
	return data.State, data.AuthURL, nil
}

func workbuddyAwaitToken(ctx context.Context, site workbuddySite, state string) (*workbuddyTokenData, error) {
	deadline := time.Now().Add(workbuddyPollTimeout)
	for {
		token, pending, err := workbuddyPollToken(ctx, site, state)
		if err != nil {
			return nil, err
		}
		if !pending {
			return token, nil
		}
		if !time.Now().Before(deadline) {
			return nil, anyerr.Newf(
				"WorkBuddy sign-in timed out after five minutes.",
				"PROVIDER_LOGIN_TIMEOUT",
				anyerr.WithSuggestions("Try Sign In again and approve it in the browser within five minutes."))
		}
		select {
		case <-ctx.Done():
			return nil, anyerr.Newf("WorkBuddy sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		case <-time.After(workbuddyPollInterval):
		}
	}
}

func workbuddyPollToken(ctx context.Context, site workbuddySite, state string) (*workbuddyTokenData, bool, error) {
	req, err := http.NewRequest(http.MethodGet,
		site.Base+workbuddyTokenPath+"?state="+url.QueryEscape(state), nil)
	if err != nil {
		return nil, false, anyerr.Wrapf(err, "build the CodeBuddy token request", "PROVIDER_LOGIN_FAILED")
	}
	req.Header.Set("X-Domain", site.Domain)
	req.Header.Set("X-Product", "SaaS")

	env, err := workbuddyFetch(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if env.Code == workbuddyCodePending {
		return nil, true, nil
	}
	if env.Code != 0 {
		return nil, false, anyerr.Newf(workbuddyServiceMessage(env), "PROVIDER_LOGIN_FAILED")
	}
	var data workbuddyTokenData
	if json.Unmarshal(env.Data, &data) != nil || data.AccessToken == "" {
		return nil, false, anyerr.Newf(
			"CodeBuddy sign-in finished without an access token.",
			"PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions("Try Sign In again and complete every step in the browser."))
	}
	return &data, false, nil
}

// workbuddyFetch performs one request and returns the service envelope
// uninterpreted: the token endpoint uses non-zero codes for "not yet", so only
// the caller can decide what a code means.
func workbuddyFetch(ctx context.Context, req *http.Request) (*workbuddyEnvelope, error) {
	req.Header.Set("User-Agent", workbuddyUserAgent)
	req.Header.Set("X-Request-ID", workbuddyRequestID())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil, anyerr.Newf("WorkBuddy sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		}
		return nil, anyerr.Wrapf(err, "reach the CodeBuddy sign-in service", "PROVIDER_LOGIN_FAILED")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, anyerr.Wrapf(err, "read the CodeBuddy sign-in response", "PROVIDER_LOGIN_FAILED")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, anyerr.Newf(
			fmt.Sprintf("CodeBuddy sign-in service returned HTTP %d.", resp.StatusCode),
			"PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions("Try Sign In again; if it keeps failing the service may have changed."))
	}
	var env workbuddyEnvelope
	if json.Unmarshal(body, &env) != nil {
		return nil, anyerr.Newf(
			"CodeBuddy sign-in service returned an unreadable response.",
			"PROVIDER_LOGIN_FAILED")
	}
	return &env, nil
}

func workbuddyServiceMessage(env *workbuddyEnvelope) string {
	msg := strings.TrimSpace(env.Msg)
	if msg == "" {
		return fmt.Sprintf("CodeBuddy sign-in failed (code %d).", env.Code)
	}
	return fmt.Sprintf("CodeBuddy sign-in failed (code %d): %s", env.Code, msg)
}

// workbuddyTokenSubject reads the `sub` claim of an access token. It is the
// only stable per-account identifier the service returns, and AnyPick uses it
// to re-save an existing account instead of aliasing a second one.
func workbuddyTokenSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Sub
}

// openBrowserURL hands a sign-in URL to the desktop browser. Only https URLs
// are accepted: the value comes from a remote service and is passed to a
// platform launcher.
func openBrowserURL(raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return errors.New("refusing to open a non-https URL")
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

func workbuddyRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("anypick-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
