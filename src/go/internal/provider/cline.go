package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/atomicfile"
	"github.com/outof0/anypick/go/internal/protocol"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/transport"
)

// cline is the Cline subscription account source (ClinePass and Cline
// usage billing share one WorkOS login; see cline_oauth.go).
//
// Two files are involved and only one is authoritative:
//
//	~/.cline-anypick/auth.json                    AnyPick staging (Login writes)
//	~/.anypick/providers/cline/accounts/<name>/…  the snapshot
//
// ~/.cline/data/settings/providers.json (Cline's own store) is read for
// DetectLive/Backup fallback only and never written: its
// ProviderSettingsManager owns atomic writes there.
type cline struct{ home string }

func newCline(home string) *cline { return &cline{home: home} }

const (
	clineAuthFile    = "auth.json"
	clineAuthVersion = 1
	clineKindSub     = "subscription"

	clineLoginMethodID = "cline-pass"
)

// clineAuthDocument is the token document AnyPick writes and reads.
type clineAuthDocument struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	MethodID    string `json:"methodId,omitempty"`
	AccessToken string `json:"accessToken,omitempty"`
	// RefreshToken is written and read: short-lived Cline access tokens are
	// refreshed per request through clineValidCredentials, so an expired
	// access token never means signing in again while the grant is alive.
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAtMs  int64  `json:"expiresAtMs,omitempty"`
	Email        string `json:"email,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	ObtainedAt   string `json:"obtainedAt,omitempty"`
}

func (p *cline) ID() string          { return "cline" }
func (p *cline) Name() string        { return "Cline" }
func (p *cline) ShortName() string   { return "Cline" }
func (p *cline) Description() string { return "ClinePass and Cline subscription accounts" }
func (p *cline) CanProxy() bool      { return true }

// NativeClientID names the first-party harness the login belongs to. No Go
// client adapter is registered for cline yet; this id is reserved for it and
// transport policy comes from AccountAdapter.
func (p *cline) NativeClientID() string { return "cline" }

func (p *cline) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.ClineAccountAdapter(p.ID(), accountName)
}

// authPath is where AnyPick keeps its staging credential.
//
// It deliberately ignores Cline's own config roots: those move between the
// app, the CLI and test fixtures, and honouring them would make the same
// account read as logged in or not depending on where the command was typed
// (the same trap workbuddy.authPath documents). Nothing outside AnyPick
// opens this file, so a fixed path is the only stable one.
// CLINE_AUTH_PATH stays as an explicit escape hatch for tests.
func (p *cline) authPath() string {
	if env := strings.TrimSpace(os.Getenv("CLINE_AUTH_PATH")); env != "" {
		return expandHome(env)
	}
	return filepath.Join(expandHome(p.home), ".cline-anypick", clineAuthFile)
}

// clineExternalProvidersPath is Cline's own credential store, read-only.
func clineExternalProvidersPath(home string) string {
	if env := strings.TrimSpace(os.Getenv("CLINE_PROVIDERS_PATH")); env != "" {
		return expandHome(env)
	}
	root := expandHome(home)
	if root == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			root = userHome
		}
	}
	return filepath.Join(root, ".cline", "data", "settings", "providers.json")
}

func clineJWTEmail(token string) string {
	trimmed := strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(trimmed), "workos:") {
		trimmed = strings.TrimSpace(trimmed[7:])
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) < 2 {
		return ""
	}
	segment := parts[1]
	payload, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(segment)
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Email == "" {
		return ""
	}
	return strings.TrimSpace(claims.Email)
}

// readClineAuthDocument returns nil without an error when the file is missing
// or is not a credential AnyPick wrote. A file we cannot parse is not a login.
func readClineAuthDocument(path string) (*clineAuthDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc clineAuthDocument
	if json.Unmarshal(raw, &doc) != nil || (doc.AccessToken == "" && doc.RefreshToken == "") {
		return nil, nil
	}
	if doc.Email == "" {
		doc.Email = clineJWTEmail(doc.AccessToken)
	}
	return &doc, nil
}

// persistClineRefresh writes a rotated grant back onto the snapshot and the
// in-memory set the next request will refresh from. Dropping the new refresh
// token leaves the saved login dead the moment the issuer invalidates the old
// one, and a stale expiry refreshes on every request after that.
func persistClineRefresh(path string, doc *clineAuthDocument, cur, valid *clineCredentials) error {
	if doc == nil || cur == nil || valid == nil {
		return nil
	}
	if valid.Access == cur.Access && valid.Refresh == cur.Refresh && valid.ExpiresMs == cur.ExpiresMs {
		return nil
	}
	next := *cur
	nextDoc := *doc
	if valid.Access != "" {
		next.Access = valid.Access
		nextDoc.AccessToken = valid.Access
	}
	if valid.Refresh != "" {
		next.Refresh = valid.Refresh
		nextDoc.RefreshToken = valid.Refresh
	}
	next.ExpiresMs = valid.ExpiresMs
	nextDoc.ExpiresAtMs = valid.ExpiresMs
	if valid.Email != "" {
		next.Email = valid.Email
		nextDoc.Email = valid.Email
	}
	if valid.AccountID != "" {
		next.AccountID = valid.AccountID
		nextDoc.AccountID = valid.AccountID
	}
	if err := writeClineAuthDocument(path, nextDoc); err != nil {
		return err
	}
	*cur = next
	*doc = nextDoc
	return nil
}

func writeClineAuthDocument(path string, doc clineAuthDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// path is either AnyPick staging or the live Cline auth doc (ADR 0004):
	// replace by rename in both cases so one helper cannot regress.
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

// readClineExternalCredential reads Cline's own providers.json without ever
// writing it. Observed live layout (2026-09): credentials sit at
// providers.<id>.settings.auth; the shared "cline" key carries the WorkOS
// login (cline-pass merges it on read, so it is the fallback). The access
// token carries a "workos:" prefix that is not part of the bearer.
func readClineExternalCredential(path string) (*clineAuthDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type clineFileAuth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
		AccountID    string `json:"accountId"`
		Email        string `json:"email"`
		Metadata     *struct {
			UserInfo *struct {
				Email string `json:"email"`
			} `json:"userInfo"`
		} `json:"metadata"`
	}
	type clineFileEntry struct {
		Auth     *clineFileAuth `json:"auth"`
		Settings *struct {
			Auth *clineFileAuth `json:"auth"`
		} `json:"settings"`
	}
	var doc struct {
		Providers map[string]clineFileEntry `json:"providers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, nil
	}
	authOf := func(entry clineFileEntry) *clineFileAuth {
		if entry.Settings != nil && entry.Settings.Auth != nil {
			return entry.Settings.Auth
		}
		return entry.Auth
	}
	var auth *clineFileAuth
	if entry, ok := doc.Providers["cline"]; ok {
		auth = authOf(entry)
	}
	if auth == nil {
		if entry, ok := doc.Providers["cline-pass"]; ok {
			auth = authOf(entry)
		}
	}
	if auth == nil || (auth.AccessToken == "" && auth.RefreshToken == "") {
		return nil, nil
	}
	var metadataEmail string
	if auth.Metadata != nil && auth.Metadata.UserInfo != nil {
		metadataEmail = auth.Metadata.UserInfo.Email
	}
	email := firstNonEmpty(auth.Email, metadataEmail, clineJWTEmail(auth.AccessToken))
	return &clineAuthDocument{
		Version:  clineAuthVersion,
		Kind:     clineKindSub,
		MethodID: clineLoginMethodID,
		// Stored raw: the gateway bearer carries the "workos:" namespace
		// (Cline's own formatter re-adds it when missing), so stripping it
		// here is what earned an "Unauthorized" on the first live probe.
		AccessToken:  auth.AccessToken,
		RefreshToken: auth.RefreshToken,
		ExpiresAtMs:  auth.ExpiresAt,
		Email:        email,
		AccountID:    auth.AccountID,
	}, nil
}

func (p *cline) DetectLive() (LiveAuthStatus, error) {
	if doc, err := readClineAuthDocument(p.authPath()); err != nil {
		return LiveAuthStatus{}, err
	} else if doc != nil {
		identity := firstNonEmpty(doc.Email, doc.AccountID)
		return LiveAuthStatus{Present: true, Identity: identity}, nil
	}
	external, err := readClineExternalCredential(clineExternalProvidersPath(p.home))
	if err != nil {
		return LiveAuthStatus{}, err
	}
	if external == nil {
		return LiveAuthStatus{}, nil
	}
	return LiveAuthStatus{Present: true, Identity: firstNonEmpty(external.Email, external.AccountID)}, nil
}

func (p *cline) LoginMethods() []LoginMethod {
	return []LoginMethod{{
		ID:          clineLoginMethodID,
		Label:       "ClinePass subscription",
		Description: "Browser sign-in for ClinePass and Cline usage billing",
		// The WorkOS device flow is Cline's own, but the token reuse from a
		// third-party harness is undocumented, so users should know AnyPick
		// is on its own here.
		Provenance: "community",
		Preferred:  true,
		Available:  true,
	}}
}

func (p *cline) Login(ctx context.Context, methodID, source string) error {
	if source != "" {
		return anyerr.Newf(
			"Cline sign-in has no separate sources.",
			"PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	if methodID != clineLoginMethodID {
		return anyerr.Newf("Unknown Cline sign-in method.", "PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	creds, err := clineDeviceLogin(ctx)
	if err != nil {
		return err
	}
	doc := clineAuthDocument{
		Version:      clineAuthVersion,
		Kind:         clineKindSub,
		MethodID:     clineLoginMethodID,
		AccessToken:  creds.Access,
		RefreshToken: creds.Refresh,
		ExpiresAtMs:  creds.ExpiresMs,
		Email:        creds.Email,
		AccountID:    creds.AccountID,
		ObtainedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	return writeClineAuthDocument(p.authPath(), doc)
}

// ClearLive drops the staging file. Cline's own providers.json is never
// touched: signing the user out of their Cline app is not AnyPick's call.
func (p *cline) ClearLive() error {
	err := os.Remove(p.authPath())
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// Backup reports proxy-only so core never restores this credential into a
// live client: nothing reads ~/.cline-anypick/auth.json, so writing it back
// would only clobber AnyPick's own staging file. Broad checkpoints prefer the
// external Cline store so an older staging file cannot hide a login the user
// changed outside AnyPick.
func (p *cline) Backup(destDir string) (SnapshotInfo, error) {
	external, err := readClineExternalCredential(clineExternalProvidersPath(p.home))
	if err != nil {
		return SnapshotInfo{}, err
	}
	doc := external
	if doc == nil {
		doc, err = readClineAuthDocument(p.authPath())
		if err != nil {
			return SnapshotInfo{}, err
		}
	}
	if doc == nil {
		return SnapshotInfo{}, anyerr.Newf(
			"No live Cline login to save.",
			"NO_LIVE_AUTH",
			anyerr.WithSuggestions("Run `anypick login cline` first."))
	}
	doc.MethodID = clineLoginMethodID
	if err := writeClineAuthDocument(filepath.Join(destDir, clineAuthFile), *doc); err != nil {
		return SnapshotInfo{}, err
	}
	return SnapshotInfo{
		Identity:        firstNonEmpty(doc.Email, doc.AccountID),
		CredentialKind:  "proxy-only",
		CredentialClass: "external_oauth",
	}, nil
}

func (p *cline) Restore(srcDir string) error {
	doc, err := readClineAuthDocument(filepath.Join(srcDir, clineAuthFile))
	if err != nil {
		return err
	}
	if doc == nil {
		return errSnapshotMissing(clineAuthFile)
	}
	return writeClineAuthDocument(p.authPath(), *doc)
}

func (p *cline) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	doc, err := readClineAuthDocument(filepath.Join(srcDir, clineAuthFile))
	if err != nil {
		return SnapshotInfo{}, err
	}
	if doc == nil {
		return SnapshotInfo{}, nil
	}
	identity := doc.Email
	if identity == "" && doc.AccountID != "" {
		if ext, _ := readClineExternalCredential(clineExternalProvidersPath(p.home)); ext != nil && ext.AccountID == doc.AccountID && ext.Email != "" {
			identity = ext.Email
		}
	}
	return SnapshotInfo{
		Identity:        firstNonEmpty(identity, doc.AccountID),
		CredentialKind:  "proxy-only",
		CredentialClass: "external_oauth",
	}, nil
}

// ValidateSnapshot fails fast on a definitively dead grant. Cline access
// tokens are short-lived by design, so expiry alone never fails: the refresh
// path is probed, and only an invalid-grant rejection means signing in again.
// Transport errors fail open — the vendor decides at request time.
func (p *cline) ValidateSnapshot(snapshotDir string) error {
	doc, err := readClineAuthDocument(filepath.Join(snapshotDir, clineAuthFile))
	if err != nil || doc == nil || (doc.AccessToken == "" && doc.RefreshToken == "") {
		// Presence is ValidateCredential's check, not this one.
		return err
	}
	cur := &clineCredentials{
		Access: doc.AccessToken, Refresh: doc.RefreshToken,
		ExpiresMs: doc.ExpiresAtMs, AccountID: doc.AccountID, Email: doc.Email,
	}
	ctx, cancel := context.WithTimeout(context.Background(), clineHTTPTimeout)
	defer cancel()
	valid, err := clineValidCredentials(ctx, cur)
	if err != nil {
		if errors.Is(err, ErrClineReauth) {
			return anyerr.Newf(
				"The saved Cline login was rejected. Sign in again to repair it.",
				"AUTH_REQUIRED",
				anyerr.WithExitCode(anyerr.AuthRequired),
				anyerr.WithSuggestions("anypick add account cline --new --name <name>"))
		}
		return nil
	}
	return persistClineRefresh(filepath.Join(snapshotDir, clineAuthFile), doc, cur, valid)
}

// ResolveUpstream points the shared OpenAI-compatible loopback proxy at the
// Cline gateway. The gateway speaks OpenAI Chat Completions under a versioned
// base, so the inbound /v1/… path must never reach it verbatim.
func (p *cline) ResolveUpstream(snapshotDir string) (*proxy.Upstream, error) {
	doc, err := readClineAuthDocument(filepath.Join(snapshotDir, clineAuthFile))
	if err != nil {
		return nil, err
	}
	if doc == nil || (doc.AccessToken == "" && doc.RefreshToken == "") {
		return nil, anyerr.Newf(
			"No Cline credential in this snapshot.", "NO_CREDENTIAL",
			anyerr.WithSuggestions("Save a subscription login: `anypick login cline`."))
	}
	// CLINE_API_BASE_URL overrides the whole upstream base, including
	// /api/v1. It exists for testing against a local stand-in.
	base := strings.TrimSpace(os.Getenv("CLINE_API_BASE_URL"))
	if base == "" {
		base = clineAPIBase() + "/api/v1"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, anyerr.Wrapf(err, "parse Cline upstream base", "PROXY_START_FAILED")
	}
	if !u.IsAbs() || u.Hostname() == "" {
		return nil, anyerr.Newf(
			"The saved Cline login has no usable endpoint. Sign in again to repair it.",
			"PROXY_START_FAILED",
			anyerr.WithSuggestions("anypick add account cline --new --name <name>"),
		)
	}
	cur := &clineCredentials{
		Access: doc.AccessToken, Refresh: doc.RefreshToken,
		ExpiresMs: doc.ExpiresAtMs, AccountID: doc.AccountID, Email: doc.Email,
	}
	up := &proxy.Upstream{
		BaseURL:         u,
		Auth:            doc.AccessToken,
		AuthHeader:      "Authorization",
		AuthValuePrefix: "Bearer ",
		AuthProvider: func(ctx context.Context) (string, error) {
			valid, err := clineValidCredentials(ctx, cur)
			if err != nil {
				return "", err
			}
			if err := persistClineRefresh(filepath.Join(snapshotDir, clineAuthFile), doc, cur, valid); err != nil {
				return "", err
			}
			return clineBearerToken(cur.Access), nil
		},
		TranslateAnthropic: true,
		// The gateway serves chat under …/api/v1/chat/completions: the
		// inbound /v1/… path must never reach the vendor verbatim.
		OpenAIPath: "/chat/completions",
		// No vendor-specific branding rewrites: unlike the subscription
		// channels that reject foreign CLI fingerprints, no such filter is
		// verified for Cline, so only shared billing hygiene applies.
		SanitizeOpenAIBody: protocol.SanitizeOpenAIBody,
		QuotaErrorPatterns: []string{
			"quota", "rate limit", "rate_limit", "insufficient",
			"not subscribed", "pass limit", "limit exceeded", "billing",
		},
	}
	up.ModelDiscovery = compatibleModelDiscovery(up, u.String())
	return up, nil
}
