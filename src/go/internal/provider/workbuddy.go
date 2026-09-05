package provider

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/protocol"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/transport"
)

// workbuddy is the WorkBuddy AI / CodeBuddy subscription account source.
//
// It is the one provider here that cannot borrow an official CLI to sign in:
// no CodeBuddy CLI ships on the user's machine, and the desktop app keeps its
// token behind Electron safeStorage, reachable only by asking the running
// daemon — which raises a Keychain prompt and blocks for tens of seconds. So
// AnyPick runs Tencent's own OAuth device flow itself, the same one the
// official CLI uses, and owns the resulting token end to end.
//
// Two files are involved and only one is authoritative:
//
//	~/.workbuddy/auth.json                             live, staging
//	~/.anypick/providers/workbuddy/accounts/<name>/…   the snapshot
//
// The live file exists only because core's login sequence is
// Login → DetectLive → Backup with no way to skip the middle step, and Login
// is not handed a destination directory. Nothing outside AnyPick reads it:
// WorkBuddy has no code path that opens auth.json. Every consumer, including
// the proxy, reads the snapshot.
//
// API-key accounts are deliberately not supported here — the `tokenhub`
// compatible provider already covers Tencent's pay-as-you-go and Coding Plan
// keys with the real endpoints.
type workbuddy struct{ home string }

func newWorkBuddy(home string) *workbuddy { return &workbuddy{home: home} }

const (
	workbuddyAuthFile    = "auth.json"
	workbuddyAuthVersion = 1
	workbuddyKindSub     = "subscription"
)

// workbuddyAuthDocument is the token document AnyPick writes and reads.
type workbuddyAuthDocument struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	MethodID    string `json:"methodId,omitempty"`
	Base        string `json:"base,omitempty"`
	Domain      string `json:"domain,omitempty"`
	AccessToken string `json:"accessToken,omitempty"`
	// RefreshToken is written but never read: AnyPick has no token-refresh
	// machinery at all — no Refresher interface, no expiry column in the
	// account meta — so an expired token currently means signing in again.
	// It is kept so adding refresh later does not force every existing
	// account through a full browser re-login.
	RefreshToken string `json:"refreshToken,omitempty"`
	UserID       string `json:"userId,omitempty"`
	ObtainedAt   string `json:"obtainedAt,omitempty"`
}

func (p *workbuddy) ID() string          { return "workbuddy" }
func (p *workbuddy) Name() string        { return "WorkBuddy AI" }
func (p *workbuddy) ShortName() string   { return "WorkBuddy" }
func (p *workbuddy) Description() string { return "WorkBuddy AI and CodeBuddy subscription accounts" }
func (p *workbuddy) CanProxy() bool      { return true }

func (p *workbuddy) AccountAdapter(accountName string, _ bool) transport.Adapter {
	return transport.WorkBuddyAccountAdapter(p.ID(), accountName)
}

// authPath is where AnyPick keeps its staging credential.
//
// It deliberately ignores WORKBUDDY_CONFIG_DIR and CODEBUDDY_CONFIG_DIR.
// Those are the WorkBuddy app's own variables and it exports them into every
// process it spawns, so honouring them would put this file under
// ~/.workbuddy-ai when AnyPick runs inside the app and under ~/.workbuddy
// when it runs from a plain Terminal — the same account would read as logged
// in or not depending on where the command was typed. Nothing outside AnyPick
// opens this file, so a fixed path costs nothing and is the only stable one.
// WORKBUDDY_AUTH_PATH stays as an explicit escape hatch for tests.
func (p *workbuddy) authPath() string {
	if env := strings.TrimSpace(os.Getenv("WORKBUDDY_AUTH_PATH")); env != "" {
		return expandHome(env)
	}
	return filepath.Join(expandHome(p.home), ".workbuddy", workbuddyAuthFile)
}

// readAuthDocument returns nil without an error when the file is missing or is
// not a credential AnyPick wrote. A file we cannot parse is not a login.
func readWorkBuddyAuthDocument(path string) (*workbuddyAuthDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc workbuddyAuthDocument
	if json.Unmarshal(raw, &doc) != nil || doc.AccessToken == "" {
		return nil, nil
	}
	return &doc, nil
}

func writeWorkBuddyAuthDocument(path string, doc workbuddyAuthDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (p *workbuddy) DetectLive() (LiveAuthStatus, error) {
	doc, err := readWorkBuddyAuthDocument(p.authPath())
	if err != nil {
		return LiveAuthStatus{}, err
	}
	if doc == nil {
		return LiveAuthStatus{}, nil
	}
	return LiveAuthStatus{Present: true, Identity: doc.UserID}, nil
}

func (p *workbuddy) LoginMethods() []LoginMethod {
	methods := make([]LoginMethod, 0, len(workbuddySites))
	for _, site := range workbuddySites {
		methods = append(methods, LoginMethod{
			ID:          site.MethodID,
			Label:       site.Label,
			Description: site.Description,
			// Not "official": these endpoints are undocumented and Tencent
			// can change them without notice, so users should know AnyPick
			// is on its own here.
			Provenance: "community",
			Preferred:  site.Preferred,
			// The flow is plain HTTPS against a public service; there is no
			// launcher binary that could be missing.
			Available: true,
		})
	}
	return methods
}

func (p *workbuddy) Login(ctx context.Context, methodID, source string) error {
	if source != "" {
		return anyerr.Newf(
			"WorkBuddy sign-in has no separate sources.",
			"PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	site, ok := workbuddySiteByMethod(methodID)
	if !ok {
		return anyerr.Newf("Unknown WorkBuddy sign-in method.", "PROVIDER_LOGIN_METHOD_UNKNOWN")
	}
	token, err := workbuddyDeviceLogin(ctx, site)
	if err != nil {
		return err
	}
	domain := token.Domain
	if domain == "" {
		domain = site.Domain
	}
	doc := workbuddyAuthDocument{
		Version:      workbuddyAuthVersion,
		Kind:         workbuddyKindSub,
		MethodID:     site.MethodID,
		Base:         site.Base,
		Domain:       domain,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		UserID:       workbuddyTokenSubject(token.AccessToken),
		ObtainedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	return writeWorkBuddyAuthDocument(p.authPath(), doc)
}

// ClearLive drops the staging file. There is no server-side logout to call:
// revoking a subscription session is not part of the endpoint we use.
func (p *workbuddy) ClearLive() error {
	err := os.Remove(p.authPath())
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// Backup reports proxy-only so core never restores this credential into a live
// client: nothing reads ~/.workbuddy/auth.json, so writing it back would only
// clobber AnyPick's own staging file.
func (p *workbuddy) Backup(destDir string) (SnapshotInfo, error) {
	doc, err := readWorkBuddyAuthDocument(p.authPath())
	if err != nil {
		return SnapshotInfo{}, err
	}
	if doc == nil {
		return SnapshotInfo{}, anyerr.Newf(
			"No live WorkBuddy login to save.",
			"NO_LIVE_AUTH",
			anyerr.WithSuggestions("Run `anypick login workbuddy` first."))
	}
	if err := writeWorkBuddyAuthDocument(filepath.Join(destDir, workbuddyAuthFile), *doc); err != nil {
		return SnapshotInfo{}, err
	}
	return SnapshotInfo{
		Identity:        doc.UserID,
		CredentialKind:  "proxy-only",
		CredentialClass: "external_oauth",
	}, nil
}

func (p *workbuddy) Restore(srcDir string) error {
	doc, err := readWorkBuddyAuthDocument(filepath.Join(srcDir, workbuddyAuthFile))
	if err != nil {
		return err
	}
	if doc == nil {
		return errSnapshotMissing(workbuddyAuthFile)
	}
	return writeWorkBuddyAuthDocument(p.authPath(), *doc)
}

func (p *workbuddy) DescribeSnapshot(srcDir string) (SnapshotInfo, error) {
	doc, err := readWorkBuddyAuthDocument(filepath.Join(srcDir, workbuddyAuthFile))
	if err != nil {
		return SnapshotInfo{}, err
	}
	if doc == nil {
		return SnapshotInfo{}, nil
	}
	return SnapshotInfo{
		Identity:        doc.UserID,
		CredentialKind:  "proxy-only",
		CredentialClass: "external_oauth",
	}, nil
}

// ResolveUpstream points the shared OpenAI-compatible loopback proxy at the
// CodeBuddy chat endpoint. The host comes from the credential, not from a
// package default: a token issued by copilot.tencent.com is rejected by
// www.codebuddy.ai, so guessing would be wrong for half the users.
func (p *workbuddy) ResolveUpstream(snapshotDir string) (*proxy.Upstream, error) {
	doc, err := readWorkBuddyAuthDocument(filepath.Join(snapshotDir, workbuddyAuthFile))
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.AccessToken == "" {
		return nil, anyerr.Newf(
			"No WorkBuddy credential in this snapshot.", "NO_CREDENTIAL",
			anyerr.WithSuggestions("Save a subscription login: `anypick login workbuddy`."))
	}
	// WORKBUDDY_BASE_URL overrides the whole upstream base, including /v2.
	// It exists for testing against a local stand-in.
	base := strings.TrimSpace(os.Getenv("WORKBUDDY_BASE_URL"))
	if base == "" {
		base = doc.Base + "/v2"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, anyerr.Wrapf(err, "parse WorkBuddy upstream base", "PROXY_START_FAILED")
	}
	// A snapshot written before the issuing host was recorded (or a blank
	// Base) parses as a path-only URL with no host. The loopback health
	// check would still pass and the failure would only surface on the first
	// forwarded request with a confusing error — fail here instead, while the
	// user can still act on it.
	if !u.IsAbs() || u.Hostname() == "" {
		return nil, anyerr.Newf(
			"The saved WorkBuddy login has no usable endpoint. Sign in again to repair it.",
			"PROXY_START_FAILED",
			anyerr.WithSuggestions("anypick login workbuddy"),
		)
	}
	domain := strings.TrimSpace(doc.Domain)
	if domain == "" {
		if site, ok := workbuddySiteByMethod(doc.MethodID); ok {
			domain = site.Domain
		}
	}
	headers := []proxy.ExtraHeader{
		{Name: "User-Agent", Value: workbuddyUserAgent},
		{Name: "X-Product", Value: "SaaS"},
	}
	if domain != "" {
		headers = append(headers, proxy.ExtraHeader{Name: "X-Domain", Value: domain})
	}
	return &proxy.Upstream{
		BaseURL:            u,
		Auth:               doc.AccessToken,
		AuthHeader:         "Authorization",
		AuthValuePrefix:    "Bearer ",
		ExtraHeaders:       headers,
		TranslateAnthropic: true,
		// The subscription chat surface is …/v2/chat/completions: the
		// inbound /v1/… path must never reach the vendor, which reads it
		// as an API-key channel marker and rejects the call (code 11128).
		OpenAIPath: "/chat/completions",
		// Claude Code's system prompt carries an x-anthropic-billing-header
		// line the vendor's filter reads as channel spoofing (same 11128).
		// Subscription chat also requires a system-first message. Model id
		// is left untouched: the caller must send a slug the vendor
		// actually serves.
		SanitizeOpenAIBody: workbuddySanitizeOpenAIBody,
		QuotaErrorPatterns: []string{"quota", "balance", "insufficient", "billing"},
	}, nil
}

// workbuddySanitizeOpenAIBody is the subscription-channel rewrite: billing
// boilerplate out, and a system message prepended when Claude Code sent
// only user blocks (vendor 11128 otherwise). The model id is not rewritten
// — inventing a slug would break the day the vendor ships that name.
func workbuddySanitizeOpenAIBody(body []byte) []byte {
	body = protocol.SanitizeOpenAIBody(body)
	var v map[string]interface{}
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	msgs, _ := v["messages"].([]interface{})
	if !workbuddyHasLeadingSystem(msgs) {
		sys := map[string]interface{}{
			"role":    "system",
			"content": "You are a helpful coding assistant.",
		}
		v["messages"] = append([]interface{}{sys}, msgs...)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

func workbuddyHasLeadingSystem(msgs []interface{}) bool {
	if len(msgs) == 0 {
		return false
	}
	mm, ok := msgs[0].(map[string]interface{})
	if !ok {
		return false
	}
	role, _ := mm["role"].(string)
	return role == "system"
}
