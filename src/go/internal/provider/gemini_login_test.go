package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeminiLoginOAuthWritesTheSelectedSource(t *testing.T) {
	t.Setenv("GEMINI_TOKEN_ENDPOINT", "")
	originalAuthorization := geminiOAuthAuthorizationEndpoint
	originalUserInfo := geminiOAuthUserInfoEndpoint
	originalOpenBrowser := geminiOAuthOpenBrowser
	originalLoginTimeout := geminiOAuthLoginTimeout
	originalHTTPTimeout := geminiOAuthHTTPTimeout
	t.Cleanup(func() {
		geminiOAuthAuthorizationEndpoint = originalAuthorization
		geminiOAuthUserInfoEndpoint = originalUserInfo
		geminiOAuthOpenBrowser = originalOpenBrowser
		geminiOAuthLoginTimeout = originalLoginTimeout
		geminiOAuthHTTPTimeout = originalHTTPTimeout
	})

	for _, tc := range []struct {
		name       string
		methodID   string
		source     string
		credential string
		email      string
	}{
		{
			name:       "gemini cli",
			methodID:   geminiLoginMethodID,
			source:     geminiCliOAuthSource,
			credential: "oauth_creds.json",
			email:      "cli@example.com",
		},
		{
			name:       "antigravity",
			methodID:   antigravityLoginMethodID,
			source:     antigravityOAuthSource,
			credential: "antigravity_oauth.json",
			email:      "antigravity@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".gemini")
			t.Setenv("GEMINI_CONFIG_DIR", configDir)

			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/token" {
					http.NotFound(w, r)
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm: %v", err)
				}
				if got := r.Form.Get("code"); got != "test-code" {
					t.Errorf("code = %q", got)
				}
				if got := r.Form.Get("grant_type"); got != "authorization_code" {
					t.Errorf("grant_type = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600,"scope":"scope","id_token":"header.`+testJWTEmail(t, tc.email)+`.signature"}`)
			}))
			defer tokenServer.Close()
			t.Setenv("GEMINI_TOKEN_ENDPOINT", tokenServer.URL+"/token")

			authorizationServer := httptest.NewServer(http.NotFoundHandler())
			defer authorizationServer.Close()
			geminiOAuthAuthorizationEndpoint = authorizationServer.URL
			geminiOAuthUserInfoEndpoint = ""
			geminiOAuthLoginTimeout = 2 * time.Second
			geminiOAuthHTTPTimeout = time.Second
			browserDone := make(chan error, 1)
			geminiOAuthOpenBrowser = func(raw string) error {
				u, err := url.Parse(raw)
				if err != nil {
					return err
				}
				callback, err := url.Parse(u.Query().Get("redirect_uri"))
				if err != nil {
					return err
				}
				if callback.Hostname() != "127.0.0.1" {
					t.Errorf("callback host = %q, want the bound IPv4 loopback", callback.Host)
				}
				// The production server binds 127.0.0.1 but the registered
				// Google redirect URI uses the same explicit loopback address.
				callback.Host = strings.Replace(callback.Host, "localhost", "127.0.0.1", 1)
				query := callback.Query()
				query.Set("state", u.Query().Get("state"))
				query.Set("code", "test-code")
				callback.RawQuery = query.Encode()
				client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return http.ErrUseLastResponse
				}}
				go func() {
					resp, err := client.Get(callback.String())
					if err != nil {
						browserDone <- err
						return
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						browserDone <- &loginTestError{message: "callback did not redirect"}
						return
					}
					browserDone <- nil
				}()
				return nil
			}

			if err := newGemini(home).Login(context.Background(), tc.methodID, tc.source); err != nil {
				t.Fatalf("Login: %v", err)
			}
			select {
			case err := <-browserDone:
				if err != nil {
					t.Fatalf("callback: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("callback browser request did not finish")
			}
			if _, err := os.Stat(filepath.Join(configDir, tc.credential)); err != nil {
				t.Fatalf("selected credential missing: %v", err)
			}
			other := "oauth_creds.json"
			if tc.credential == other {
				other = "antigravity_oauth.json"
			}
			if _, err := os.Stat(filepath.Join(configDir, other)); !os.IsNotExist(err) {
				t.Fatalf("unselected credential exists: %v", err)
			}
			status, err := newGemini(home).DetectLiveSource(tc.source)
			if err != nil || !status.Present || status.Identity != tc.email {
				t.Fatalf("selected live status = %+v, err=%v", status, err)
			}
		})
	}
}

func TestGeminiLoginRejectsForeignSource(t *testing.T) {
	err := newGemini(t.TempDir()).Login(context.Background(), geminiLoginMethodID, antigravityOAuthSource)
	if err == nil || !strings.Contains(err.Error(), "Unknown Gemini sign-in method") {
		t.Fatalf("foreign source error = %v", err)
	}
}

func TestGeminiLoginOAuthRejectsIncompleteTokenWithoutWritingCredential(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".gemini")
	t.Setenv("GEMINI_CONFIG_DIR", configDir)
	t.Setenv("GEMINI_TOKEN_ENDPOINT", "")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"access_token":"old-access","refresh_token":"old-refresh","token_type":"Bearer"}`)
	oldPath := filepath.Join(configDir, "oauth_creds.json")
	if err := os.WriteFile(oldPath, old, 0o600); err != nil {
		t.Fatal(err)
	}
	originalAuthorization := geminiOAuthAuthorizationEndpoint
	originalUserInfo := geminiOAuthUserInfoEndpoint
	originalOpenBrowser := geminiOAuthOpenBrowser
	originalLoginTimeout := geminiOAuthLoginTimeout
	originalHTTPTimeout := geminiOAuthHTTPTimeout
	t.Cleanup(func() {
		geminiOAuthAuthorizationEndpoint = originalAuthorization
		geminiOAuthUserInfoEndpoint = originalUserInfo
		geminiOAuthOpenBrowser = originalOpenBrowser
		geminiOAuthLoginTimeout = originalLoginTimeout
		geminiOAuthHTTPTimeout = originalHTTPTimeout
	})
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access-only"}`)
	}))
	defer tokenServer.Close()
	t.Setenv("GEMINI_TOKEN_ENDPOINT", tokenServer.URL)
	authorizationServer := httptest.NewServer(http.NotFoundHandler())
	defer authorizationServer.Close()
	geminiOAuthAuthorizationEndpoint = authorizationServer.URL
	geminiOAuthUserInfoEndpoint = ""
	geminiOAuthLoginTimeout = 2 * time.Second
	geminiOAuthHTTPTimeout = time.Second
	browserDone := make(chan error, 1)
	geminiOAuthOpenBrowser = func(raw string) error {
		u, _ := url.Parse(raw)
		callback, _ := url.Parse(u.Query().Get("redirect_uri"))
		callback.Host = strings.Replace(callback.Host, "localhost", "127.0.0.1", 1)
		query := callback.Query()
		query.Set("state", u.Query().Get("state"))
		query.Set("code", "test-code")
		callback.RawQuery = query.Encode()
		client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		go func() {
			resp, err := client.Get(callback.String())
			if err != nil {
				browserDone <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				browserDone <- &loginTestError{message: "failed exchange did not return the local failure page"}
				return
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "Google sign-in failed") {
				browserDone <- &loginTestError{message: "failed exchange page did not explain the failure"}
				return
			}
			browserDone <- nil
		}()
		return nil
	}

	err := newGemini(t.TempDir()).Login(context.Background(), geminiLoginMethodID, geminiCliOAuthSource)
	select {
	case browserErr := <-browserDone:
		if browserErr != nil {
			t.Fatalf("callback: %v", browserErr)
		}
	case <-time.After(time.Second):
		t.Fatal("callback browser request did not finish")
	}
	if err == nil || !strings.Contains(err.Error(), "incomplete sign-in credential") {
		t.Fatalf("incomplete token error = %v", err)
	}
	got, readErr := os.ReadFile(oldPath)
	if readErr != nil || string(got) != string(old) {
		t.Fatalf("existing credential changed after failed exchange: %v %q", readErr, got)
	}
}

type loginTestError struct{ message string }

func (e *loginTestError) Error() string { return e.message }

func testJWTEmail(t *testing.T, email string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}
