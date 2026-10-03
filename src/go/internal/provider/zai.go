package provider

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/transport"
)

// ZCode is an Electron client, but its login protocol is provider-owned: the
// app stores an encrypted Z.ai OAuth token and exposes an official CLI polling
// flow. Keeping the implementation here means the host never needs to know
// ZCode's cipher, endpoints, or token exchange details.
const (
	zaiSourceID = "zcode"
	// zaiStagingSourceID names the AnyPick-owned staging copy the browser login
	// writes. It is distinct from zaiSourceID (ZCode's external store) so a
	// clear can remove AnyPick's own login without ever claiming to sign the
	// user out of ZCode.
	zaiStagingSourceID    = "anypick"
	zaiLoginMethodID      = "zai-subscription"
	zaiSnapshotFile       = "zai-credential.json"
	zaiLiveFile           = "zai-live.json"
	zaiCredentialsEnv     = "ANYPICK_ZAI_CREDENTIALS_PATH"
	zaiLiveEnv            = "ANYPICK_ZAI_LIVE_PATH"
	zaiOAuthInitEnv       = "ZAI_OAUTH_INIT_URL"
	zaiOAuthPollEnv       = "ZAI_OAUTH_POLL_URL"
	zaiBusinessLoginEnv   = "ZAI_BUSINESS_LOGIN_URL"
	zaiOAuthInitURL       = "https://zcode.z.ai/api/v1/oauth/cli/init"
	zaiOAuthPollURL       = "https://zcode.z.ai/api/v1/oauth/cli/poll/"
	zaiBusinessLoginURL   = "https://api.z.ai/api/auth/z/login"
	zaiOAuthCredentialKey = "oauth:zai:access_token"
)

var (
	// These seams keep the real browser flow testable without ever putting a
	// credential or a remote provider call in a unit test.
	zaiOAuthHTTPClient  = func() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
	zaiOAuthOpenBrowser = openBrowserURL
	zaiOAuthPollTimeout = 5 * time.Minute
)

type zai struct {
	*Compatible
	home string

	tokenMu              sync.Mutex
	cachedOAuthToken     string
	cachedBusinessToken  string
	cachedBusinessExpiry time.Time
}

// newZAI keeps the API-key definition and the native subscription behavior on
// one provider value. The definition is validated during registry assembly;
// a failure here therefore indicates a programmer error in the built-in table,
// not user-controlled state.
func newZAI(home string) (*zai, error) {
	for _, def := range BuiltinDefinitions() {
		if def.ID != "zai" {
			continue
		}
		compatible, err := NewCompatible(def)
		if err != nil {
			return nil, err
		}
		return &zai{Compatible: compatible, home: home}, nil
	}
	return nil, anyerr.Newf("missing built-in zai definition", "INVALID_USAGE")
}

func (p *zai) LoginMethods() []LoginMethod {
	return []LoginMethod{{
		ID:          zaiLoginMethodID,
		Label:       "Z.ai subscription",
		Description: "Official Z.ai browser sign-in through ZCode",
		Provenance:  "official",
		Preferred:   true,
		Source:      zaiSourceID,
		SourceLabel: "ZCode",
		Available:   true,
	}}
}

// NativeClientID is intentionally ZCode rather than zai: Z.ai is the
// upstream account provider, while ZCode is the first-party app the user can
// point at that login.
func (p *zai) NativeClientID() string { return "zcode" }

// AccountAdapter extends the compatible-provider defaults with the one
// first-party client that can consume a Z.ai subscription account. Keeping
// this provider-owned avoids teaching transport or the desktop host about the
// zai ↔ zcode relationship.
func (p *zai) AccountAdapter(accountName string, proxyEnabled bool) transport.Adapter {
	a := p.Compatible.AccountAdapter(accountName, proxyEnabled)
	a.Capabilities.NativeClients = []string{"zcode"}
	previous := a.TransportFor
	a.TransportFor = func(clientID string) transport.Capability {
		if clientID == "zcode" {
			return transport.ManagedBuiltinProxy
		}
		if previous != nil {
			return previous(clientID)
		}
		return transport.Unsupported
	}
	return a
}

func (p *zai) Login(ctx context.Context, methodID, source string) error {
	if methodID != zaiLoginMethodID || (source != "" && source != zaiSourceID && source != zaiStagingSourceID) {
		return anyerr.Newf("Unknown Z.ai sign-in method.", "PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	return p.loginOAuth(ctx)
}

// zaiCredential is the small, AnyPick-owned credential document used for a
// snapshot and for the temporary live staging file. The ZCode encrypted store
// is never copied or rewritten.
type zaiCredential struct {
	Version             int    `json:"version"`
	Source              string `json:"source"`
	OAuthAccessToken    string `json:"oauthAccessToken"`
	BusinessAccessToken string `json:"businessAccessToken,omitempty"`
	BusinessExpiresAt   int64  `json:"businessExpiresAt,omitempty"`
	Identity            string `json:"identity,omitempty"`
	UserID              string `json:"userId,omitempty"`
}

func (p *zai) credentialsPath() string {
	if value := strings.TrimSpace(os.Getenv(zaiCredentialsEnv)); value != "" {
		return expandHome(value)
	}
	return filepath.Join(expandHome(p.home), ".zcode", "v2", "credentials.json")
}

func (p *zai) livePath() string {
	if value := strings.TrimSpace(os.Getenv(zaiLiveEnv)); value != "" {
		return expandHome(value)
	}
	root := strings.TrimSpace(os.Getenv("ANYPICK_HOME"))
	if root == "" {
		root = filepath.Join(expandHome(p.home), ".anypick")
	} else {
		root = expandHome(root)
	}
	return filepath.Join(root, "providers", "zai", zaiLiveFile)
}

func (p *zai) DetectLive() (LiveAuthStatus, error) {
	credential, err := p.readExternalCredential()
	if err != nil {
		// Presence is useful to the UI even when a future ZCode encryption
		// revision is not readable yet. Backup/Login will return the actionable
		// error; detection must not make the provider disappear entirely.
		if _, statErr := os.Stat(p.credentialsPath()); statErr == nil {
			return LiveAuthStatus{
				Present: true, Source: zaiSourceID,
				Details: "ZCode login is present but unreadable",
			}, nil
		}
		return LiveAuthStatus{}, err
	}
	if credential != nil {
		return zaiLiveStatus(*credential, "ZCode login", zaiSourceID), nil
	}

	credential, err = p.readLiveCredential()
	if err != nil {
		return LiveAuthStatus{
			Present: true, Source: zaiStagingSourceID,
			Details: "AnyPick Z.ai login is unreadable",
		}, nil
	}
	if credential == nil {
		return LiveAuthStatus{}, nil
	}
	return zaiLiveStatus(*credential, "AnyPick Z.ai login", zaiStagingSourceID), nil
}

func zaiLiveStatus(credential zaiCredential, details, source string) LiveAuthStatus {
	return LiveAuthStatus{
		Present:   true,
		Identity:  credential.Identity,
		AccountID: credential.UserID,
		Source:    source,
		Details:   details,
	}
}

func (p *zai) DetectLiveSource(source string) (LiveAuthStatus, error) {
	if source != "" && source != zaiSourceID && source != zaiStagingSourceID {
		return LiveAuthStatus{}, anyerr.Newf(
			"Z.ai has no live login source \""+source+"\".",
			"INVALID_USAGE")
	}
	// A browser login writes AnyPick's staging file because the HTTPS web flow
	// cannot safely write ZCode's encrypted Electron store. Source-scoped
	// checks prefer that freshly completed login, then fall back to ZCode's
	// existing local session.
	credential, err := p.readLiveCredential()
	if err != nil {
		return LiveAuthStatus{
			Present: true, Source: zaiStagingSourceID,
			Details: "AnyPick Z.ai login is unreadable",
		}, nil
	}
	if credential != nil {
		return zaiLiveStatus(*credential, "AnyPick Z.ai login", zaiStagingSourceID), nil
	}
	return p.DetectLive()
}

func (p *zai) Backup(destDir string) (SnapshotInfo, error) {
	// Broad checkpoints prefer the external ZCode store so an older AnyPick
	// staging file cannot hide a login the user changed outside AnyPick.
	return p.backupCredential(destDir, false)
}

func (p *zai) BackupSource(source, destDir string) (SnapshotInfo, error) {
	if source != "" && source != zaiSourceID && source != zaiStagingSourceID {
		return SnapshotInfo{}, anyerr.Newf(
			"Z.ai cannot save live source \""+source+"\".",
			"INVALID_USAGE")
	}
	return p.backupCredential(destDir, true)
}

func (p *zai) backupCredential(destDir string, preferStage bool) (SnapshotInfo, error) {
	var credential *zaiCredential
	var err error
	if preferStage {
		// The browser flow writes this staging file after a successful login. It
		// must win for the post-login save, otherwise an older ZCode session could
		// be snapshotted under the newly selected account.
		credential, err = p.readLiveCredential()
	} else {
		credential, err = p.readExternalCredential()
	}
	if err != nil {
		return SnapshotInfo{}, zaiNoLiveAuth(err)
	}
	if credential == nil {
		if preferStage {
			credential, err = p.readExternalCredential()
		} else {
			credential, err = p.readLiveCredential()
		}
		if err != nil {
			return SnapshotInfo{}, zaiNoLiveAuth(err)
		}
	}
	if credential == nil {
		return SnapshotInfo{}, zaiNoLiveAuth(nil)
	}
	credential.Source = zaiSourceID
	credential.Version = 1
	if err := writeZAICredential(filepath.Join(destDir, zaiSnapshotFile), *credential); err != nil {
		return SnapshotInfo{}, err
	}
	return zaiSnapshotInfo(*credential), nil
}

func zaiNoLiveAuth(cause error) error {
	message := "No usable ZCode login was found on this Mac."
	if cause != nil {
		message = "The ZCode login could not be read safely."
	}
	return anyerr.Newf(message, "NO_LIVE_AUTH",
		anyerr.WithSuggestions(
			"Sign in to ZCode again, or choose New Account to sign in through Z.ai.",
			"You can also use a GLM Coding Plan key from the Z.ai dashboard."))
}

// CanClearLive only authorises removing AnyPick's own staging copy
// (zaiStagingSourceID). ZCode's external credential is never advertised as
// clearable: signing out of ZCode is not AnyPick's to do.
func (p *zai) CanClearLive(source string) bool { return source == zaiStagingSourceID }

func (p *zai) ClearLive() error {
	err := os.Remove(p.livePath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *zai) Restore(srcDir string) error {
	path := filepath.Join(srcDir, zaiSnapshotFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			// API-key accounts remain on the compatible path and have no live
			// file to restore.
			return p.Compatible.Restore(srcDir)
		}
		return err
	}
	credential, err := readZAICredential(path)
	if err != nil {
		return err
	}
	return writeZAICredential(p.livePath(), *credential)
}

func (p *zai) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	path := filepath.Join(srcDir, zaiSnapshotFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return p.Compatible.DescribeSnapshot(srcDir)
		}
		return SnapshotInfo{}, err
	}
	credential, err := readZAICredential(path)
	if err != nil {
		return SnapshotInfo{}, err
	}
	return zaiSnapshotInfo(*credential), nil
}

func zaiSnapshotInfo(credential zaiCredential) SnapshotInfo {
	return SnapshotInfo{
		Identity:        credential.Identity,
		Notes:           "ZCode subscription login",
		CredentialKind:  "proxy-only",
		CredentialClass: "external_oauth",
	}
}

// ValidateSnapshot probes the saved OAuth token with a bounded business-token
// exchange: the token carries no exp claim, so only the server can call it
// dead. A definitive rejection fails here, before any mutation; transport
// errors fail open. API-key snapshots have nothing to probe and are skipped.
func (p *zai) ValidateSnapshot(snapshotDir string) error {
	if _, err := os.Stat(filepath.Join(snapshotDir, compatibleCredentialFile)); err == nil {
		return nil
	}
	credential, err := readZAICredential(filepath.Join(snapshotDir, zaiSnapshotFile))
	if err != nil || credential == nil || credential.OAuthAccessToken == "" {
		// Presence is ValidateCredential's check, not this one.
		return nil
	}
	endpoint, err := zaiEndpoint(zaiBusinessLoginEnv, zaiBusinessLoginURL)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env, err := zaiJSONRequest(ctx, http.MethodPost, endpoint,
		map[string]string{"token": credential.OAuthAccessToken}, "")
	if err != nil {
		return nil
	}
	if zaiCodeOK(env.Code) {
		return nil
	}
	msg := strings.TrimSpace(env.Msg)
	if msg == "" {
		msg = "the Z.ai sign-in service rejected the saved login"
	} else {
		msg = "Z.ai rejected the saved login (" + msg + ")"
	}
	name := filepath.Base(filepath.Dir(snapshotDir))
	suggestions := []string{"Sign in to ZCode again, then save the login."}
	if name != "" && name != "." && name != string(filepath.Separator) {
		suggestions = append(suggestions,
			"anypick add account zai --name "+name+" --force")
	}
	return anyerr.Newf(msg+". Sign in again to repair it.",
		"AUTH_REQUIRED",
		anyerr.WithExitCode(anyerr.AuthRequired),
		anyerr.WithSuggestions(suggestions...))
}

func (p *zai) ResolveUpstream(snapshotDir string) (*proxy.Upstream, error) {
	// Keep typed API-key accounts exactly on the established compatible path.
	if _, err := os.Stat(filepath.Join(snapshotDir, compatibleCredentialFile)); err == nil {
		return p.Compatible.ResolveUpstream(snapshotDir)
	}

	credential, err := readZAICredential(filepath.Join(snapshotDir, zaiSnapshotFile))
	if err != nil {
		return nil, err
	}
	profile := p.endpointProfile(p.credentialDefinition("subscription-key"), "global")
	if profile == nil {
		return nil, anyerr.Newf("Z.ai subscription endpoint is not configured.", "STATE_CONFLICT")
	}
	base, err := url.Parse(profile.OpenAIBase)
	if err != nil {
		return nil, anyerr.Wrapf(err, "parse Z.ai upstream base", "PROXY_START_FAILED")
	}
	anthropicBase, err := url.Parse(profile.AnthropicBase)
	if err != nil {
		return nil, anyerr.Wrapf(err, "parse Z.ai Anthropic base", "PROXY_START_FAILED")
	}
	authProvider := func(ctx context.Context) (string, error) {
		return p.businessToken(ctx, *credential)
	}
	up := &proxy.Upstream{
		BaseURL:            base,
		Auth:               credential.BusinessAccessToken,
		AuthHeader:         "Authorization",
		AuthValuePrefix:    "Bearer ",
		AuthProvider:       authProvider,
		QuotaErrorPatterns: p.Def.QuotaErrorPatterns,
		ModelDiscovery:     nil,
		Anthropic: &proxy.UpstreamTarget{
			BaseURL:         anthropicBase,
			AuthHeader:      "x-api-key",
			AuthProvider:    authProvider,
			AuthValuePrefix: "",
		},
	}
	up.ModelDiscovery = compatibleModelDiscovery(up, profile.OpenAIBase)
	return up, nil
}

func (p *zai) businessToken(ctx context.Context, credential zaiCredential) (string, error) {
	if credential.OAuthAccessToken == "" {
		if credential.BusinessAccessToken != "" {
			return credential.BusinessAccessToken, nil
		}
		return "", anyerr.Newf("Z.ai subscription credential has no OAuth token.", "NO_CREDENTIAL")
	}
	now := time.Now()
	p.tokenMu.Lock()
	if p.cachedOAuthToken == credential.OAuthAccessToken &&
		p.cachedBusinessToken != "" && now.Before(p.cachedBusinessExpiry) {
		token := p.cachedBusinessToken
		p.tokenMu.Unlock()
		return token, nil
	}
	p.tokenMu.Unlock()

	token, expiry, err := zaiExchangeBusinessToken(ctx, credential.OAuthAccessToken)
	if err != nil {
		return "", err
	}
	p.tokenMu.Lock()
	p.cachedOAuthToken = credential.OAuthAccessToken
	p.cachedBusinessToken = token
	p.cachedBusinessExpiry = expiry
	p.tokenMu.Unlock()
	return token, nil
}

func (p *zai) readExternalCredential() (*zaiCredential, error) {
	raw, err := os.ReadFile(p.credentialsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	active, err := zaiStoredString(entries, "oauth:active_provider", p.home, false)
	if err != nil {
		return nil, err
	}
	if active != "" && active != "zai" {
		return nil, nil
	}
	oauthToken, err := zaiStoredString(entries, zaiOAuthCredentialKey, p.home, true)
	if err != nil {
		return nil, err
	}
	if oauthToken == "" {
		return nil, nil
	}

	identity, userID := "", ""
	if userInfo, infoErr := zaiStoredString(entries, "oauth:zai:user_info", p.home, false); infoErr == nil {
		var info struct {
			Email  string `json:"email"`
			Name   string `json:"name"`
			UserID string `json:"user_id"`
		}
		if json.Unmarshal([]byte(userInfo), &info) == nil {
			identity = firstNonEmpty(info.Email, info.Name, info.UserID)
			userID = info.UserID
		}
	}
	return &zaiCredential{
		Version:          1,
		Source:           zaiSourceID,
		OAuthAccessToken: oauthToken,
		Identity:         identity,
		UserID:           userID,
	}, nil
}

func zaiStoredString(entries map[string]json.RawMessage, key, home string, _ bool) (string, error) {
	raw, ok := entries[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	return decryptZAICredential(value, home)
}

// decryptZAICredential mirrors ZCode's safeStorage-compatible enc:v1 format:
// AES-256-GCM, a 12-byte nonce, a detached 16-byte tag, and base64url parts.
func decryptZAICredential(value, home string) (string, error) {
	if !strings.HasPrefix(value, "enc:v1:") {
		// Older development builds used plaintext values. Accepting one here is
		// limited to known ZCode keys and lets users migrate without a dead UI.
		return value, nil
	}
	parts := strings.Split(strings.TrimPrefix(value, "enc:v1:"), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid ZCode encrypted credential")
	}
	decode := func(part string) ([]byte, error) {
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		if err == nil {
			return decoded, nil
		}
		return base64.URLEncoding.DecodeString(part)
	}
	nonce, err := decode(parts[0])
	if err != nil {
		return "", err
	}
	tag, err := decode(parts[1])
	if err != nil {
		return "", err
	}
	ciphertext, err := decode(parts[2])
	if err != nil {
		return "", err
	}
	secret := os.Getenv("ZCODE_CREDENTIAL_SECRET")
	if secret == "" {
		secret = fmt.Sprintf("zcode-credential-fallback:%s:%s:%s",
			runtime.GOOS, expandHome(home), currentUsername())
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return "", fmt.Errorf("invalid ZCode encrypted credential dimensions")
	}
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt ZCode credential: %w", err)
	}
	return string(plain), nil
}

func (p *zai) readLiveCredential() (*zaiCredential, error) {
	return readZAICredential(p.livePath())
}

func readZAICredential(path string) (*zaiCredential, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var credential zaiCredential
	if err := json.Unmarshal(raw, &credential); err != nil ||
		credential.Version != 1 || credential.OAuthAccessToken == "" {
		return nil, anyerr.Newf("Invalid Z.ai credential snapshot.", "SNAPSHOT_INVALID")
	}
	return &credential, nil
}

func writeZAICredential(path string, credential zaiCredential) error {
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}

type zaiEnvelope struct {
	Code json.RawMessage `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type zaiInitData struct {
	FlowID          string      `json:"flow_id"`
	PollToken       string      `json:"poll_token"`
	AuthorizeURL    string      `json:"authorize_url"`
	ExpiresAt       json.Number `json:"expires_at"`
	PollIntervalSec json.Number `json:"poll_interval_sec"`
}

type zaiPollData struct {
	Status string `json:"status"`
	User   struct {
		Email  string `json:"email"`
		Name   string `json:"name"`
		UserID string `json:"user_id"`
	} `json:"user"`
	Zai struct {
		AccessToken string `json:"access_token"`
	} `json:"zai"`
	AccessToken string `json:"access_token"`
}

type zaiBusinessData struct {
	AccessToken  string      `json:"access_token"`
	AccessToken2 string      `json:"accessToken"`
	Token        string      `json:"token"`
	ExpiresIn    json.Number `json:"expires_in"`
}

func (p *zai) loginOAuth(ctx context.Context) error {
	initData, err := zaiStartOAuth(ctx)
	if err != nil {
		return err
	}
	authorizeURL, err := zaiAuthorizeURL(initData.AuthorizeURL)
	if err != nil {
		return anyerr.Wrapf(err, "validate the Z.ai sign-in URL", "PROVIDER_LOGIN_FAILED")
	}
	pollURL, err := zaiEndpoint(zaiOAuthPollEnv, zaiOAuthPollURL)
	if err != nil {
		return err
	}
	if err := zaiOpenBrowser(authorizeURL); err != nil {
		return err
	}
	return p.awaitOAuth(ctx, pollURL, initData.PollToken, initData)
}

func zaiStartOAuth(ctx context.Context) (zaiInitData, error) {
	pollToken, err := zaiRandomToken()
	if err != nil {
		return zaiInitData{}, anyerr.Wrapf(err, "create the Z.ai sign-in request", "PROVIDER_LOGIN_FAILED")
	}
	initURL, err := zaiEndpoint(zaiOAuthInitEnv, zaiOAuthInitURL)
	if err != nil {
		return zaiInitData{}, err
	}
	env, err := zaiJSONRequest(ctx, http.MethodPost, initURL,
		map[string]string{"provider": "zai"}, pollToken)
	if err != nil {
		return zaiInitData{}, err
	}
	if !zaiCodeOK(env.Code) {
		return zaiInitData{}, zaiServiceError(env, "Z.ai sign-in initialization")
	}
	var initData zaiInitData
	if json.Unmarshal(env.Data, &initData) != nil || initData.FlowID == "" ||
		initData.PollToken == "" || initData.AuthorizeURL == "" {
		return zaiInitData{}, anyerr.Newf("Z.ai sign-in did not return a complete login request.", "PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions("Try Sign In again; if it keeps failing, update ZCode."))
	}
	return initData, nil
}

func zaiOpenBrowser(authorizeURL string) error {
	if err := zaiOAuthOpenBrowser(authorizeURL); err != nil {
		// A desktop mutation has no visible stderr. Continuing to poll after
		// open() failed stranded the sheet until timeout with no way to find
		// the one-time URL, so surface a retryable error to the native UI.
		return anyerr.Newf(
			"Could not open the Z.ai sign-in page.",
			"PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions(
				"Set a default browser, then try Sign In again.",
				"You can also sign in to ZCode first and choose On This Mac."))
	}
	return nil
}

func (p *zai) awaitOAuth(ctx context.Context, pollURL, pollToken string, initData zaiInitData) error {
	deadline := time.Now().Add(zaiOAuthPollTimeout)
	if expiresAt := zaiUnixTime(initData.ExpiresAt); !expiresAt.IsZero() && expiresAt.Before(deadline) {
		deadline = expiresAt
	}
	interval := zaiPollInterval(initData.PollIntervalSec)
	for {
		if ctx.Err() != nil {
			return anyerr.Newf("Z.ai sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		}
		if !time.Now().Before(deadline) {
			return anyerr.Newf("Z.ai sign-in timed out.", "PROVIDER_LOGIN_TIMEOUT",
				anyerr.WithSuggestions("Try Sign In again and approve it in the browser before the link expires."))
		}
		poll, pending, err := zaiPollOAuth(ctx, pollURL, initData.FlowID, pollToken)
		if err != nil {
			return err
		}
		if !pending {
			businessToken, expires, err := zaiExchangeBusinessToken(ctx, poll.Zai.AccessToken)
			if err != nil {
				return err
			}
			identity := firstNonEmpty(poll.User.Email, poll.User.Name, poll.User.UserID)
			return writeZAICredential(p.livePath(), zaiCredential{
				Version:             1,
				Source:              zaiSourceID,
				OAuthAccessToken:    poll.Zai.AccessToken,
				BusinessAccessToken: businessToken,
				BusinessExpiresAt:   expires.Unix(),
				Identity:            identity,
				UserID:              poll.User.UserID,
			})
		}
		wait := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !wait.Stop() {
				<-wait.C
			}
			return anyerr.Newf("Z.ai sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		case <-wait.C:
		}
	}
}

func zaiPollOAuth(ctx context.Context, base, flowID, pollToken string) (zaiPollData, bool, error) {
	u, err := url.Parse(base)
	if err != nil {
		return zaiPollData{}, false, anyerr.Wrapf(err, "build the Z.ai sign-in poll", "PROVIDER_LOGIN_FAILED")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(flowID)
	env, err := zaiJSONRequest(ctx, http.MethodGet, u.String(), nil, pollToken)
	if err != nil {
		return zaiPollData{}, false, err
	}
	if !zaiCodeOK(env.Code) {
		return zaiPollData{}, false, zaiServiceError(env, "Z.ai sign-in poll")
	}
	var data zaiPollData
	if json.Unmarshal(env.Data, &data) != nil {
		return zaiPollData{}, false, anyerr.Newf("Z.ai sign-in returned an unreadable status.", "PROVIDER_LOGIN_FAILED")
	}
	switch strings.ToLower(strings.TrimSpace(data.Status)) {
	case "pending", "":
		return data, true, nil
	case "failed", "rejected", "cancelled":
		return zaiPollData{}, false, anyerr.Newf("Z.ai sign-in was not approved.", "PROVIDER_LOGIN_FAILED",
			anyerr.WithSuggestions("Try Sign In again and approve the Z.ai request in the browser."))
	case "ready", "success", "completed":
		if data.Zai.AccessToken == "" {
			data.Zai.AccessToken = data.AccessToken
		}
		if data.Zai.AccessToken == "" {
			return zaiPollData{}, false, anyerr.Newf("Z.ai sign-in finished without an access token.", "PROVIDER_LOGIN_FAILED")
		}
		return data, false, nil
	default:
		return zaiPollData{}, false, anyerr.Newf("Z.ai sign-in returned an unknown status.", "PROVIDER_LOGIN_FAILED")
	}
}

func zaiExchangeBusinessToken(ctx context.Context, oauthToken string) (string, time.Time, error) {
	if oauthToken == "" {
		return "", time.Time{}, anyerr.Newf("Z.ai sign-in finished without an OAuth token.", "PROVIDER_LOGIN_FAILED")
	}
	endpoint, err := zaiEndpoint(zaiBusinessLoginEnv, zaiBusinessLoginURL)
	if err != nil {
		return "", time.Time{}, err
	}
	env, err := zaiJSONRequest(ctx, http.MethodPost, endpoint,
		map[string]string{"token": oauthToken}, "")
	if err != nil {
		return "", time.Time{}, err
	}
	if !zaiCodeOK(env.Code) {
		return "", time.Time{}, zaiServiceError(env, "Z.ai business login")
	}
	var data zaiBusinessData
	if json.Unmarshal(env.Data, &data) != nil {
		return "", time.Time{}, anyerr.Newf("Z.ai business login returned an unreadable response.", "PROVIDER_LOGIN_FAILED")
	}
	token := firstNonEmpty(data.AccessToken, data.AccessToken2, data.Token)
	if token == "" {
		return "", time.Time{}, anyerr.Newf("Z.ai business login returned no access token.", "PROVIDER_LOGIN_FAILED")
	}
	expires := time.Now().Add(10 * time.Minute)
	if seconds := zaiNumberFloat(data.ExpiresIn); seconds > 0 {
		expires = time.Now().Add(time.Duration(seconds * float64(time.Second)))
	}
	return token, expires, nil
}

func zaiJSONRequest(ctx context.Context, method, endpoint string, body any, bearer string) (*zaiEnvelope, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, anyerr.Wrapf(err, "encode the Z.ai sign-in request", "PROVIDER_LOGIN_FAILED")
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, anyerr.Wrapf(err, "build the Z.ai sign-in request", "PROVIDER_LOGIN_FAILED")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := zaiOAuthHTTPClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, anyerr.Newf("Z.ai sign-in was cancelled.", "PROVIDER_LOGIN_CANCELLED")
		}
		return nil, anyerr.Wrapf(err, "reach the Z.ai sign-in service", "PROVIDER_LOGIN_FAILED")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, anyerr.Wrapf(err, "read the Z.ai sign-in response", "PROVIDER_LOGIN_FAILED")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, anyerr.Newf(fmt.Sprintf("Z.ai sign-in service returned HTTP %d.", resp.StatusCode), "PROVIDER_LOGIN_FAILED")
	}
	var env zaiEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, anyerr.Newf("Z.ai sign-in service returned an unreadable response.", "PROVIDER_LOGIN_FAILED")
	}
	return &env, nil
}

func zaiEndpoint(envName, fallback string) (string, error) {
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return fallback, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !zaiLoopbackURL(u)) {
		return "", anyerr.Newf("Refusing an unsafe Z.ai endpoint override.", "INVALID_USAGE")
	}
	return u.String(), nil
}

func zaiLoopbackURL(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}

func zaiAuthorizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("z.ai returned a non-HTTPS authorization URL")
	}
	return u.String(), nil
}

func zaiRandomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func zaiPollInterval(value json.Number) time.Duration {
	seconds := zaiNumberFloat(value)
	if seconds <= 0 {
		seconds = 2
	}
	if seconds > 30 {
		seconds = 30
	}
	return time.Duration(seconds * float64(time.Second))
}

func zaiUnixTime(value json.Number) time.Time {
	seconds := zaiNumberFloat(value)
	if seconds <= 0 {
		return time.Time{}
	}
	if seconds > 1e12 {
		return time.UnixMilli(int64(seconds))
	}
	return time.Unix(int64(seconds), 0)
}

func zaiNumberFloat(value json.Number) float64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(string(value), 64)
	if err != nil {
		return 0
	}
	return parsed
}

func zaiCodeOK(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number == 0 || number == 200
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text == "" || text == "0" || text == "200"
	}
	return false
}

func zaiServiceError(env *zaiEnvelope, operation string) error {
	message := strings.TrimSpace(env.Msg)
	if message == "" {
		message = operation + " failed"
	}
	return anyerr.Newf(message, "PROVIDER_LOGIN_FAILED",
		anyerr.WithSuggestions("Try Sign In again and complete every step in the browser."))
}
