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
// Writes are performed only as part of an explicit account restore/login flow,
// never from routine detection. That keeps background polling free of Keychain
// prompts while allowing an intentional switch to update the mirror that older
// Antigravity builds still read.
//
// The write goes through the same `security -i` + `add-generic-password -U -w`
// shape go-keyring uses. `-X` (hex) on that stdin line looks equivalent and
// keeps the secret off argv, but hex doubles the line: an ordinary envelope
// (access token plus id_token) is ~2.3KB as `-w` and ~4.5KB as `-X`.
// /usr/bin/security reads one interactive command into a 4096-byte buffer.
// Past that it stores a truncated password and exits 1 ("unknown command"
// plus the leftover tail). A Gemini switch then fails, and the rollback
// write fails the same way, on a keychain that is already unlocked.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// securityStdinLimit is the interactive line buffer in /usr/bin/security.
// A command of 4096 bytes including the trailing newline still round-trips;
// 4098 is split and the tool exits 1. go-keyring refuses at the same bound.
const securityStdinLimit = 4096

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

// GeminiAntigravityFile names the payload AntigravityKeyringPut receives.
//
// It is an alias, not a new type, and it exists for one reason: a replacement
// written from another package has to spell its parameter type, and cannot name
// an unexported one. The alternative — renaming the type — is 31 mechanical
// edits across 9 files, which is a lot of diff to buy a name.
type GeminiAntigravityFile = geminiAntigravityFile

// AntigravityKeyringPut updates the existing go-keyring item in place. The
// production implementation keeps the credential off argv for any envelope
// that fits in security's stdin line; a larger one is passed as hex -X
// because a longer stdin line is truncated.
//
// It is a variable, not a function, so a test can substitute a fake — and it is
// exported because that substitution has to be possible from outside this
// package. The item it writes is *global*: service "gemini", account
// "antigravity", the same entry the Antigravity IDE uses. Unlike every other
// Antigravity mirror it is not scoped by HOME or ANYPICK_HOME, so the temp
// directories a test sets up do not protect it.
//
// The default is a no-op. The composition root (internal/app) installs the
// production writer for real processes via EnableAntigravityKeyringWrites
// (ADR-0025). Test binaries keep this no-op unless a test explicitly replaces
// the hook — so a forgotten per-package guard cannot overwrite the developer's
// Keychain. This package's TestMain still pins the same no-op for clarity.
var AntigravityKeyringPut = discardAntigravityKeyring

// discardAntigravityKeyring is the safe default for AntigravityKeyringPut.
func discardAntigravityKeyring(*geminiAntigravityFile) error { return nil }

// EnableAntigravityKeyringWrites installs the production OS credential-store
// writer. Call only from the composition root for real processes; tests must
// leave the no-op default (or install their own recording fake).
func EnableAntigravityKeyringWrites() {
	AntigravityKeyringPut = writeAntigravityKeyring
}

func writeAntigravityKeyring(payload *geminiAntigravityFile) error {
	if payload == nil || strings.TrimSpace(payload.Token.RefreshToken) == "" {
		return fmt.Errorf("refusing to write Antigravity Keychain without a refresh token")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		value := antigravityKeyringPrefix + base64.StdEncoding.EncodeToString(raw)
		stdin, args := antigravityDarwinInvocation(value)
		var runErr error
		if stdin != "" {
			runErr = runSecurityStdin(stdin)
		} else {
			// Only reached when -w itself would overflow the 4096-byte line.
			// argv is the channel security still accepts; -X keeps the
			// envelope out of a plaintext `ps` listing. Truncating stdin
			// instead corrupts the item and fails the switch.
			runErr = runSecurityArgs(args)
		}
		if runErr != nil {
			return fmt.Errorf("write Antigravity Keychain item: %w", runErr)
		}
		return nil
	case "linux":
		cmd := exec.Command("secret-tool", "store", "--label=gemini",
			"service", antigravityKeyringService, "username", antigravityKeyringAccount)
		cmd.Stdin = strings.NewReader(string(raw))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("write Antigravity Secret Service item: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("writing the Antigravity credential is not supported on %s", runtime.GOOS)
	}
}

// antigravityDarwinInvocation chooses how to pass value to
// add-generic-password. stdin is the go-keyring -w command when it fits in
// security's line buffer; otherwise args is the argv form (service and
// account in the clear, the envelope hex-encoded as -X).
func antigravityDarwinInvocation(value string) (stdin string, args []string) {
	command := fmt.Sprintf(
		"add-generic-password -U -s %s -a %s -w %s\n",
		quoteSecurity(antigravityKeyringService),
		quoteSecurity(antigravityKeyringAccount),
		quoteSecurity(value))
	if len(command) <= securityStdinLimit {
		return command, nil
	}
	return "", []string{
		"add-generic-password", "-U",
		"-s", antigravityKeyringService,
		"-a", antigravityKeyringAccount,
		"-X", hex.EncodeToString([]byte(value)),
	}
}

func runSecurityStdin(command string) error {
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return securityFailure(err, out)
	}
	return nil
}

func runSecurityArgs(args []string) error {
	cmd := exec.Command("/usr/bin/security", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return securityFailure(err, out)
	}
	return nil
}

// securityFailure attaches a keychain diagnostic without the secret.
// A truncated `security -i` line reports the leftover bytes as
// `unknown command "<tail>"`, and that tail is the credential.
func securityFailure(err error, output []byte) error {
	msg := strings.TrimSpace(string(output))
	if msg == "" || strings.Contains(msg, "unknown command") || strings.Contains(msg, antigravityKeyringPrefix) {
		if strings.Contains(msg, "unknown command") {
			return fmt.Errorf("%w (security truncated the command)", err)
		}
		return err
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return fmt.Errorf("%w (%s)", err, msg)
}

// restoreAntigravityKeyring is the mutation boundary used by the provider.
// Keeping it separate from the read probe makes it impossible for routine
// detection to accidentally prompt or overwrite the user's credential store.
func restoreAntigravityKeyring(payload *geminiAntigravityFile) error {
	return AntigravityKeyringPut(payload)
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
