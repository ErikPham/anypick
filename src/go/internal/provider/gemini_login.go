package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
)

const (
	geminiLoginMethodID      = "gemini-subscription"
	antigravityLoginMethodID = "antigravity-subscription"

	geminiOAuthAuthorizationEndpointDefault = "https://accounts.google.com/o/oauth2/v2/auth"
	geminiOAuthUserInfoEndpointDefault      = "https://www.googleapis.com/oauth2/v2/userinfo"
	geminiOAuthCallbackPath                 = "/oauth2callback"
	geminiOAuthLoginTimeoutDefault          = 5 * time.Minute
	geminiOAuthHTTPTimeoutDefault           = 30 * time.Second
)

var (
	// These are variables so provider tests can exercise the real loopback
	// callback without opening a browser or sending a test token to Google.
	geminiOAuthAuthorizationEndpoint = geminiOAuthAuthorizationEndpointDefault
	geminiOAuthUserInfoEndpoint      = geminiOAuthUserInfoEndpointDefault
	geminiOAuthOpenBrowser           = openBrowserURL
	geminiOAuthLoginTimeout          = geminiOAuthLoginTimeoutDefault
	geminiOAuthHTTPTimeout           = geminiOAuthHTTPTimeoutDefault
)

var geminiOAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// LoginMethods exposes the two Google subscription stores AnyPick can own
// safely: Gemini CLI's file and the Antigravity native state/keychain mirror.
// An explicit browser login writes the selected store so the next Antigravity
// launch observes the same account as AnyPick.
func (p *gemini) LoginMethods() []LoginMethod {
	return []LoginMethod{
		{
			ID:          geminiLoginMethodID,
			Label:       "Gemini subscription",
			Description: "Google browser sign-in for Gemini CLI",
			Provenance:  "official",
			Preferred:   true,
			Source:      geminiCliOAuthSource,
			SourceLabel: "Gemini CLI",
			Available:   true,
		},
		{
			ID:          antigravityLoginMethodID,
			Label:       "Antigravity subscription",
			Description: "Google browser sign-in for Antigravity-compatible access",
			Provenance:  "official",
			Source:      antigravityOAuthSource,
			SourceLabel: "Antigravity",
			Available:   true,
		},
	}
}

// Login runs Google's installed-application OAuth flow directly. Gemini CLI
// has no login-only command, and launching its interactive TUI from a desktop
// helper is both non-deterministic and prone to reusing the current account.
func (p *gemini) Login(ctx context.Context, methodID, source string) error {
	expectedSource := geminiLoginSourceForMethod(methodID)
	if expectedSource == "" || (source != "" && source != expectedSource) {
		return anyerr.Newf("Unknown Gemini sign-in method.", "PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	return p.loginOAuth(ctx, expectedSource)
}

func geminiLoginSourceForMethod(methodID string) string {
	switch methodID {
	case geminiLoginMethodID:
		return geminiCliOAuthSource
	case antigravityLoginMethodID:
		return antigravityOAuthSource
	default:
		return ""
	}
}

type geminiOAuthCallback struct {
	err      error
	code     string
	complete chan<- error
}

func (p *gemini) loginOAuth(ctx context.Context, source string) error {
	if err := ctx.Err(); err != nil {
		return geminiLoginCancelled()
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return geminiLoginFailed("Could not start the local Google sign-in callback.")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// Advertise the same IPv4 loopback address we bound. On macOS, resolving
	// localhost may prefer ::1 while the listener only accepts 127.0.0.1;
	// Google then completes consent in the browser but the callback never
	// reaches AnyPick. Google supports 127.0.0.1 for desktop loopback OAuth.
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, geminiOAuthCallbackPath)
	state, err := geminiOAuthState()
	if err != nil {
		_ = listener.Close()
		return geminiLoginFailed("Could not start the Google sign-in flow.")
	}
	authURL, err := geminiOAuthAuthorizationURL(source, redirectURI, state)
	if err != nil {
		_ = listener.Close()
		return err
	}
	server, callbackCh, serverErrCh := geminiOAuthCallbackServer(ctx, listener, state)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = listener.Close()
	}()

	if err := geminiOAuthOpenBrowser(authURL); err != nil {
		return anyerr.Newf(
			"Could not open the Google sign-in page.",
			"PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions("Check that a default browser is configured, then try Sign In again."))
	}
	callback, err := waitForGeminiOAuthCallback(ctx, callbackCh, serverErrCh)
	if err != nil {
		return err
	}
	credentials, err := exchangeGeminiOAuthCode(ctx, source, callback.code, redirectURI)
	if err != nil {
		return completeGeminiOAuthCallback(callback, err)
	}
	if err := ctx.Err(); err != nil {
		return completeGeminiOAuthCallback(callback, geminiLoginCancelled())
	}
	email := jwtEmail(credentials.IDToken)
	if email == "" {
		email = geminiOAuthUserEmail(ctx, credentials.AccessToken)
	}
	if err := p.writeGeminiLogin(source, credentials, email); err != nil {
		return completeGeminiOAuthCallback(callback, anyerr.Wrapf(
			err, "Could not save the Google sign-in securely", "PROVIDER_LOGIN_FAILED"))
	}
	return completeGeminiOAuthCallback(callback, nil)
}

func geminiOAuthCallbackServer(
	ctx context.Context,
	listener net.Listener,
	state string,
) (*http.Server, <-chan geminiOAuthCallback, <-chan error) {
	callbackCh := make(chan geminiOAuthCallback, 1)
	server := &http.Server{Handler: geminiOAuthCallbackHandler(ctx, state, callbackCh)}
	serverErrCh := make(chan error, 1)
	go func() {
		serveErr := server.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serverErrCh <- serveErr
		}
	}()
	return server, callbackCh, serverErrCh
}

func geminiOAuthCallbackHandler(
	ctx context.Context,
	state string,
	callbackCh chan<- geminiOAuthCallback,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != geminiOAuthCallbackPath {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("state") != state {
			geminiOAuthPage(w, "Google sign-in failed", "The sign-in callback could not be verified. You can close this window and try again.")
			geminiPublishCallback(callbackCh, geminiOAuthCallback{
				err: geminiLoginFailed("Google sign-in could not be verified safely."),
			})
			return
		}
		if oauthErr := query.Get("error"); oauthErr != "" {
			geminiOAuthPage(w, "Google sign-in cancelled", "You can close this window and return to AnyPick.")
			if oauthErr == "access_denied" {
				geminiPublishCallback(callbackCh, geminiOAuthCallback{err: geminiLoginCancelled()})
			} else {
				geminiPublishCallback(callbackCh, geminiOAuthCallback{
					err: geminiLoginFailed("Google sign-in was not completed."),
				})
			}
			return
		}
		code := strings.TrimSpace(query.Get("code"))
		if code == "" {
			geminiOAuthPage(w, "Google sign-in failed", "Google returned no authorization code. You can close this window and try again.")
			geminiPublishCallback(callbackCh, geminiOAuthCallback{
				err: geminiLoginFailed("Google sign-in returned no authorization code."),
			})
			return
		}
		complete := make(chan error, 1)
		if !geminiPublishCallback(callbackCh, geminiOAuthCallback{code: code, complete: complete}) {
			geminiOAuthPage(w, "Google sign-in failed", "The sign-in request is no longer active. You can close this window and try again.")
			return
		}
		select {
		case err := <-complete:
			if err != nil {
				geminiOAuthPage(w, "Google sign-in failed", "AnyPick could not finish the sign-in. You can close this window and try again.")
				return
			}
			// Keep the completion page local. Redirecting to the Gemini Code
			// Assist documentation site made a successful desktop login look
			// like an unfinished or unrelated web flow.
			geminiOAuthPage(w, "Sign-in complete", "Your Gemini account was added to AnyPick. You can close this window and return to the app.")
		case <-ctx.Done():
			geminiOAuthPage(w, "Sign-in cancelled", "You can close this window and return to AnyPick.")
		}
	})
}

func waitForGeminiOAuthCallback(ctx context.Context, callbackCh <-chan geminiOAuthCallback, serverErrCh <-chan error) (geminiOAuthCallback, error) {
	timer := time.NewTimer(geminiOAuthLoginTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return geminiOAuthCallback{}, geminiLoginCancelled()
	case <-timer.C:
		return geminiOAuthCallback{}, anyerr.Newf(
			"Google sign-in timed out.",
			"PROVIDER_LOGIN_TIMEOUT",
			anyerr.WithSuggestions("Complete the Google browser flow within five minutes, then try again."))
	case <-serverErrCh:
		return geminiOAuthCallback{}, geminiLoginFailed("The local Google sign-in callback stopped unexpectedly.")
	case callback := <-callbackCh:
		if callback.err != nil {
			return geminiOAuthCallback{}, callback.err
		}
		return callback, nil
	}
}

func geminiOAuthState() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func geminiOAuthAuthorizationURL(source, redirectURI, state string) (string, error) {
	endpoint := strings.TrimSpace(geminiOAuthAuthorizationEndpoint)
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		(u.Scheme == "http" && !isLoopbackHost(u.Hostname())) {
		return "", anyerr.Newf(
			"Google sign-in has an invalid authorization endpoint.",
			"PROVIDER_LOGIN_FAILED")
	}
	profile := geminiOAuthProfileFor(source)
	query := u.Query()
	query.Set("client_id", profile.clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("access_type", "offline")
	query.Set("prompt", "consent select_account")
	query.Set("scope", strings.Join(geminiOAuthScopes, " "))
	query.Set("state", state)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func completeGeminiOAuthCallback(callback geminiOAuthCallback, err error) error {
	if callback.complete != nil {
		callback.complete <- err
	}
	return err
}

func geminiPublishCallback(ch chan<- geminiOAuthCallback, callback geminiOAuthCallback) bool {
	select {
	case ch <- callback:
		return true
	default:
		return false
	}
}

func geminiOAuthPage(w http.ResponseWriter, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>", title, title, message)
}

func exchangeGeminiOAuthCode(ctx context.Context, source, code, redirectURI string) (*geminiOAuthCredentials, error) {
	endpoint, err := oauthTokenEndpoint("GEMINI_TOKEN_ENDPOINT", geminiTokenEndpointDefault)
	if err != nil {
		return nil, err
	}
	profile := geminiOAuthProfileFor(source)
	form := url.Values{
		"code":          {code},
		"client_id":     {profile.clientID},
		"client_secret": {profile.clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, geminiLoginFailed("Could not prepare the Google sign-in exchange.")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: geminiOAuthHTTPTimeout}).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, geminiLoginCancelled()
		}
		return nil, geminiLoginFailed("Could not reach Google to finish sign-in.")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, geminiLoginFailed("Google rejected the sign-in exchange.")
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		IDToken      string `json:"id_token"`
		ExpiresIn    *int64 `json:"expires_in"`
	}
	if json.Unmarshal(body, &token) != nil || token.AccessToken == "" || token.RefreshToken == "" {
		return nil, geminiLoginFailed("Google returned an incomplete sign-in credential.")
	}
	expiresIn := int64(3600)
	if token.ExpiresIn != nil && *token.ExpiresIn > 0 {
		expiresIn = *token.ExpiresIn
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()
	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	scope := token.Scope
	if scope == "" {
		scope = strings.Join(geminiOAuthScopes, " ")
	}
	return &geminiOAuthCredentials{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    tokenType,
		ExpiryDate:   &expiresAt,
		Scope:        scope,
		ClientID:     profile.clientID,
		IDToken:      token.IDToken,
	}, nil
}

func geminiOAuthUserEmail(ctx context.Context, accessToken string) string {
	endpoint := strings.TrimSpace(geminiOAuthUserInfoEndpoint)
	if endpoint == "" || accessToken == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := (&http.Client{Timeout: geminiOAuthHTTPTimeout}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var info struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &info) != nil || !strings.Contains(info.Email, "@") {
		return ""
	}
	return info.Email
}

func (p *gemini) writeGeminiLogin(source string, credentials *geminiOAuthCredentials, email string) error {
	dir := p.configDir()
	profile := geminiOAuthProfileFor(source)
	credentials.ClientID = profile.clientID
	if source == antigravityOAuthSource {
		envelope := geminiAntigravityFile{AuthMethod: "consumer", Email: email}
		envelope.Token.AccessToken = credentials.AccessToken
		envelope.Token.RefreshToken = credentials.RefreshToken
		envelope.Token.TokenType = credentials.TokenType
		if credentials.ExpiryDate != nil {
			envelope.Token.Expiry = time.UnixMilli(*credentials.ExpiryDate).UTC().Format(time.RFC3339Nano)
		}
		data, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(dir, antigravityCredentialFiles[0])
		if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
			return err
		}
		// A browser login is also a native Antigravity login. Updating only the
		// portable file leaves an already-installed IDE on the previous Google
		// account, and the next source-scoped detection would read that stale
		// state back. Use the same verified Keychain/state writer as switching.
		return p.restoreAntigravity(path)
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, geminiCliCredentialFiles[0]), append(data, '\n'), 0o600)
}

func geminiLoginCancelled() error {
	return anyerr.Newf("Google sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
}

func geminiLoginFailed(message string) error {
	return anyerr.Newf(
		message,
		"PROVIDER_LOGIN_FAILED",
		anyerr.WithSuggestions("Try Sign In again and complete every step in the Google browser flow."))
}
