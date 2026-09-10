package provider

// Antigravity's second credential mirror: the OS credential store.
//
// The IDE keeps the same OAuth payload in two places — its own VS Code
// global-state database (gemini_antigravity_state.go) and the OS credential
// store, written through github.com/zalando/go-keyring under
// service "gemini", account "antigravity". On macOS the stored value is
//
//	"go-keyring-base64:" + base64(JSON)      {token:{access,refresh,type,expiry}}
//
// base64 only on darwin, so the JSON survives being handed to the `security`
// CLI. Linux (Secret Service) and Windows (Credential Manager) store the JSON
// raw; only the darwin shape is implemented here, because that is the only one
// we can verify against a real Antigravity install.
//
// Two rules are load-bearing, and both come from the reference implementation:
//
//  1. Existence is a different question from content. `security
//     find-generic-password` *without* -w lists an item's attributes and does
//     not raise the Keychain prompt; *with* -w it reads the secret and prompts
//     the first time a given binary asks. Routine detection — DetectLive,
//     anything the TUI or the desktop service polls — must use the former. A
//     background status poll that asks the user to unlock their Google refresh
//     token is not a feature.
//
//  2. So the secret is read only where the user is present and has asked for
//     exactly this: Backup, i.e. `add account`.
//
// Write-back is deliberately not ported. go-keyring's item is a mirror the
// IDE owns; the reference only rewrites it inside
// assertAntigravityStateSafeToMutate, which refuses while Antigravity runs.
// Half-porting that guard is worse than not writing at all.

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
)

const (
	antigravityKeyringService = "gemini"
	antigravityKeyringAccount = "antigravity"
	antigravityKeyringPrefix  = "go-keyring-base64:"
)

// antigravityKeyringExec runs the platform credential-store CLI.
//
// It is a variable, not a function, so a test can substitute a fake: no test
// may touch a developer's real Keychain, and the shape of the arguments is
// itself the invariant worth pinning (see the -w test below).
var antigravityKeyringExec = func(args ...string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", exec.ErrNotFound
	}
	out, err := exec.Command("/usr/bin/security", args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// antigravityKeyringExists reports whether the item is present, without
// reading its secret. A missing item and a locked or refused keychain are the
// same answer here: nothing to discover.
func antigravityKeyringExists() bool {
	_, err := antigravityKeyringExec("find-generic-password",
		"-s", antigravityKeyringService, "-a", antigravityKeyringAccount)
	return err == nil
}

// antigravityKeyringCredential reads the secret. Call it only from a path the
// user triggered: the first read for a given binary raises the macOS Keychain
// prompt.
func antigravityKeyringCredential() *geminiAntigravityFile {
	raw, err := antigravityKeyringExec("find-generic-password",
		"-s", antigravityKeyringService, "-a", antigravityKeyringAccount, "-w")
	if err != nil {
		return nil
	}
	return parseAntigravityStoreValue(raw)
}

// parseAntigravityStoreValue unwraps the platform envelope and parses the
// payload. The refresh token is the durable grant: a payload without one
// cannot be restored later, so it is not a credential we can use.
func parseAntigravityStoreValue(raw string) *geminiAntigravityFile {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, antigravityKeyringPrefix) {
		decoded, err := base64.StdEncoding.DecodeString(
			strings.TrimSpace(value[len(antigravityKeyringPrefix):]))
		if err != nil {
			return nil
		}
		value = string(decoded)
	}
	var payload geminiAntigravityFile
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.Token.RefreshToken) == "" {
		return nil
	}
	if payload.Token.TokenType == "" {
		payload.Token.TokenType = "Bearer"
	}
	return &payload
}
