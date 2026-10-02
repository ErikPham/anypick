package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/atomicfile"
)

// Native WorkBuddy/CodeBuddy desktop-app session.
//
// The app's own login lives in a plaintext JSON session file it watches and
// reconciles itself (FileAuthenticationStorage in the app bundle):
//
//	<base>/Data/Public/auth/workbuddy-desktop-ai.info
//
// where <base> is ~/Library/Application Support/CodeBuddyExtension on darwin,
// ~/AppData/Local/CodeBuddyExtension on Windows and ~/.local/share/
// CodeBuddyExtension elsewhere (the app's FilePathService.getBasePath), and
// workbuddy-desktop-ai is the authentication.id from its product.json. The
// document carries {account, auth, accounts, allAccounts}; the credential is
// auth.accessToken / auth.refreshToken. At-rest field encryption exists in the
// app behind a build flag that currently resolves to "disabled", so the file
// on disk is plain JSON — AnyPick reads and writes exactly that shape and
// never invents its own store the app would ignore.
//
// WORKBUDDY_NATIVE_AUTH_PATH overrides the path outright (tests).
const (
	workbuddyNativeAuthID   = "workbuddy-desktop-ai"
	workbuddyNativeAuthFile = "workbuddy-desktop-ai.info"
	workbuddyExtensionDir   = "CodeBuddyExtension"
)

// workbuddyNativeAuthPath reports where the desktop app keeps its session.
func workbuddyNativeAuthPath(home string) string {
	if env := strings.TrimSpace(os.Getenv("WORKBUDDY_NATIVE_AUTH_PATH")); env != "" {
		return expandHome(env)
	}
	root := expandHome(home)
	if root == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			root = userHome
		}
	}
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(root, "Library", "Application Support", workbuddyExtensionDir)
	case "windows":
		base = filepath.Join(root, "AppData", "Local", workbuddyExtensionDir)
	default:
		base = filepath.Join(root, ".local", "share", workbuddyExtensionDir)
	}
	return filepath.Join(base, "Data", "Public", "auth", workbuddyNativeAuthFile)
}

// workbuddyNativeBaseForDomain maps a session domain back to the issuing
// origin ResolveUpstream understands. The desktop app calls
// www.workbuddy.ai while device-flow tokens are issued by www.codebuddy.ai;
// China sessions stay on their own host (their tokens are rejected
// elsewhere — see workbuddyChinaOrigin).
func workbuddyNativeBaseForDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if workbuddyChinaOrigin(domain) {
		return "https://copilot.tencent.com"
	}
	return "https://" + domain
}

// readWorkBuddyNativeDoc converts the desktop app's session file into the
// snapshot document shape. It returns nil without an error when the file is
// missing or holds no access token: a file AnyPick cannot use is not a login.
func readWorkBuddyNativeDoc(path string) (*workbuddyAuthDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil, nil
	}
	var auth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		Domain       string `json:"domain"`
	}
	if rawAuth, ok := top["auth"]; ok {
		_ = json.Unmarshal(rawAuth, &auth)
	}
	if strings.TrimSpace(auth.AccessToken) == "" {
		return nil, nil
	}
	var account struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
	}
	if rawAccount, ok := top["account"]; ok {
		_ = json.Unmarshal(rawAccount, &account)
	}
	// Readable first, stable second: the token's email (or username) is what
	// listings show, so it wins over the opaque uid. The uid stays as the
	// fallback for tokens that carry no readable claims.
	userID := workbuddyTokenIdentity(auth.AccessToken)
	if userID == "" {
		userID = strings.TrimSpace(account.Nickname)
	}
	if userID == "" {
		userID = strings.TrimSpace(account.UID)
	}
	var methodID string
	if workbuddyChinaOrigin(auth.Domain) {
		methodID = "codebuddy-china"
	} else {
		methodID = "workbuddy-international"
	}
	return &workbuddyAuthDocument{
		Version:      workbuddyAuthVersion,
		Kind:         workbuddyKindSub,
		MethodID:     methodID,
		Base:         workbuddyNativeBaseForDomain(auth.Domain),
		Domain:       strings.TrimSpace(auth.Domain),
		AccessToken:  auth.AccessToken,
		RefreshToken: auth.RefreshToken,
		UserID:       userID,
	}, nil
}

// workbuddyNativeUID is the stable account id for the session merge below.
// It comes from the token's sub claim — never from the display identity, so a
// readable email identity can never overwrite the app's uid field.
func workbuddyNativeUID(doc workbuddyAuthDocument) string {
	if uid := workbuddyTokenSubject(doc.AccessToken); strings.TrimSpace(uid) != "" {
		return strings.TrimSpace(uid)
	}
	return strings.TrimSpace(doc.UserID)
}

// setNativeString sets one string field on a raw JSON object, preserving every
// other key byte-for-byte.
func setNativeString(obj map[string]json.RawMessage, key, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	obj[key] = encoded
}

// writeWorkBuddyNativeSession merges a snapshot document into the desktop
// app's session file. Fields AnyPick does not own pass through untouched; only
// the credential (auth.accessToken/refreshToken/domain) and the session
// identity (account.uid/lastLogin, mirrored into accounts/allAccounts) are
// replaced. A missing or unreadable file is rebuilt minimally so a first
// switch still leaves a session the app accepts.
func writeWorkBuddyNativeSession(path string, doc workbuddyAuthDocument) error {
	var top map[string]json.RawMessage
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &top)
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}

	account, encodedAccount, err := mergeNativeAccount(top, doc)
	if err != nil {
		return err
	}
	mergeNativeAuth(top, doc)
	mergeNativeAccountLists(top, account, encodedAccount, doc)

	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// path is the desktop app's live session file (ADR 0004): rename, not truncate.
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

// mergeNativeAuth replaces the owned credential fields, preserving the rest.
func mergeNativeAuth(top map[string]json.RawMessage, doc workbuddyAuthDocument) {
	var auth map[string]json.RawMessage
	if rawAuth, ok := top["auth"]; ok {
		_ = json.Unmarshal(rawAuth, &auth)
	}
	if auth == nil {
		auth = map[string]json.RawMessage{}
	}
	setNativeString(auth, "accessToken", doc.AccessToken)
	if strings.TrimSpace(doc.RefreshToken) != "" {
		setNativeString(auth, "refreshToken", doc.RefreshToken)
	}
	if strings.TrimSpace(doc.Domain) != "" {
		setNativeString(auth, "domain", strings.TrimSpace(doc.Domain))
	}
	if sid := workbuddyTokenSessionState(doc.AccessToken); sid != "" {
		setNativeString(auth, "sessionState", sid)
	}
	if exp, ok := workbuddyTokenExpiry(doc.AccessToken); ok {
		if encoded, err := json.Marshal(exp.UnixMilli()); err == nil {
			auth["expiresAt"] = encoded
		}
		if encoded, err := json.Marshal(time.Now().UnixMilli()); err == nil {
			auth["lastRefreshTime"] = encoded
		}
	}
	if encoded, err := json.Marshal(auth); err == nil {
		top["auth"] = encoded
	}
}

// mergeNativeAccount replaces the session identity and returns it in both
// decoded and encoded form for the list mirror below.
func mergeNativeAccount(top map[string]json.RawMessage, doc workbuddyAuthDocument) (map[string]json.RawMessage, []byte, error) {
	var account map[string]json.RawMessage
	if rawAccount, ok := top["account"]; ok {
		_ = json.Unmarshal(rawAccount, &account)
	}
	if account == nil {
		account = map[string]json.RawMessage{}
	}
	if uid := workbuddyNativeUID(doc); uid != "" {
		setNativeString(account, "uid", uid)
	}
	nickname := workbuddyTokenIdentity(doc.AccessToken)
	if nickname == "" {
		nickname = strings.TrimSpace(doc.UserID)
	}
	if nickname != "" {
		setNativeString(account, "nickname", nickname)
	}
	if encoded, err := json.Marshal(true); err == nil {
		account["lastLogin"] = encoded
	}
	if _, ok := account["type"]; !ok {
		setNativeString(account, "type", "personal")
	}
	encodedAccount, err := json.Marshal(account)
	if err != nil {
		return nil, nil, err
	}
	top["account"] = encodedAccount
	return account, encodedAccount, nil
}

// mergeNativeAccountLists mirrors the identity into the account lists the app
// renders. Entries for other logins are marked lastLogin=false; when the active
// account is not in the list, it is appended so the desktop app recognizes it.
func mergeNativeAccountLists(top, account map[string]json.RawMessage, encodedAccount []byte, doc workbuddyAuthDocument) {
	uid := workbuddyNativeUID(doc)
	for _, key := range []string{"accounts", "allAccounts"} {
		var entries []map[string]json.RawMessage
		matched := false
		if rawList, ok := top[key]; ok && json.Unmarshal(rawList, &entries) == nil {
			for _, entry := range entries {
				var entryUID string
				if rawUID, ok := entry["uid"]; ok {
					_ = json.Unmarshal(rawUID, &entryUID)
				}
				if uid != "" && entryUID == uid {
					matched = true
					for k, v := range account {
						entry[k] = v
					}
					if encoded, err := json.Marshal(true); err == nil {
						entry["lastLogin"] = encoded
					}
				} else {
					if encoded, err := json.Marshal(false); err == nil {
						entry["lastLogin"] = encoded
					}
				}
			}
		}
		if !matched {
			var fresh map[string]json.RawMessage
			if json.Unmarshal(encodedAccount, &fresh) == nil {
				entries = append(entries, fresh)
			}
		}
		if entries != nil {
			if encoded, err := json.Marshal(entries); err == nil {
				top[key] = encoded
			}
		}
	}
}
