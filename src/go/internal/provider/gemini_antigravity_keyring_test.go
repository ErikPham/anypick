package provider

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the real OS credential store out of the test process.
//
// Antigravity's keychain item is machine-global: unlike every other credential
// this package reads, it is not scoped to a home directory a test can point at
// a temp dir. Left reachable, `DetectLive` would report a Gemini login as
// present on a developer's machine that happens to be signed in to Antigravity,
// and absent in CI — a suite whose result depends on who runs it, which is the
// worst possible kind of test. Tests that mean to exercise the keyring opt back
// in by replacing the hook themselves.
func TestMain(m *testing.M) {
	antigravityKeyringExec = func(args ...string) (string, error) {
		return "", exec.ErrNotFound
	}
	os.Exit(m.Run())
}

// stubAntigravityKeyring installs a fake credential-store CLI for one test.
func stubAntigravityKeyring(t *testing.T, fn func(args ...string) (string, error)) {
	t.Helper()
	antigravityKeyringExec = fn
	t.Cleanup(func() {
		antigravityKeyringExec = func(args ...string) (string, error) {
			return "", exec.ErrNotFound
		}
	})
}

const antigravityKeyringTestPayload = `{"token":{"access_token":"ya29.access",` +
	`"refresh_token":"1//refresh","token_type":"Bearer","expiry":"2026-09-11T00:00:00Z"}}`

// The shape of the arguments is the invariant: -w is what turns a harmless
// attribute listing into a read of the user's refresh token, and that read is
// what raises the macOS Keychain prompt. A status poll must never pass it.
func TestAntigravityKeyringProbeNeverReadsTheSecret(t *testing.T) {
	var got []string
	stubAntigravityKeyring(t, func(args ...string) (string, error) {
		got = append([]string(nil), args...)
		return "", nil
	})
	if !antigravityKeyringExists() {
		t.Fatal("existence probe reported absent against a stub that succeeds")
	}
	if strings.Contains(strings.Join(got, " "), "-w") {
		t.Fatalf("existence probe passed -w (%v): it would read the secret and "+
			"raise the Keychain prompt during routine detection", got)
	}
	if want := "find-generic-password -s gemini -a antigravity"; strings.Join(got, " ") != want {
		t.Fatalf("probe args = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestAntigravityKeyringCredentialDecodesTheDarwinEnvelope(t *testing.T) {
	stubAntigravityKeyring(t, func(args ...string) (string, error) {
		return antigravityKeyringPrefix +
			base64.StdEncoding.EncodeToString([]byte(antigravityKeyringTestPayload)) + "\n", nil
	})
	cred := antigravityKeyringCredential()
	if cred == nil {
		t.Fatal("darwin go-keyring envelope did not decode")
	}
	if cred.Token.AccessToken != "ya29.access" || cred.Token.RefreshToken != "1//refresh" {
		t.Fatalf("token = %+v, want the decoded grant", cred.Token)
	}
	if cred.Token.TokenType != "Bearer" {
		t.Fatalf("token_type = %q, want Bearer", cred.Token.TokenType)
	}
}

// Linux and Windows store the JSON raw, without the darwin base64 envelope.
func TestAntigravityKeyringCredentialAcceptsARawPayload(t *testing.T) {
	stubAntigravityKeyring(t, func(args ...string) (string, error) {
		return antigravityKeyringTestPayload, nil
	})
	cred := antigravityKeyringCredential()
	if cred == nil || cred.Token.RefreshToken != "1//refresh" {
		t.Fatalf("raw payload rejected: %+v", cred)
	}
	// An unspecified type still has to come out usable by the proxy.
	if cred.Token.TokenType != "Bearer" {
		t.Fatalf("token_type = %q, want the Bearer default", cred.Token.TokenType)
	}
}

// Anything without the durable grant is not a credential: it cannot be
// restored later, so treating it as present would save an empty account.
func TestAntigravityKeyringCredentialNeedsARefreshToken(t *testing.T) {
	stubAntigravityKeyring(t, func(args ...string) (string, error) {
		return `{"token":{"access_token":"ya29.access"}}`, nil
	})
	if cred := antigravityKeyringCredential(); cred != nil {
		t.Fatalf("payload with no refresh token accepted: %+v", cred.Token)
	}
}

// The case this port exists for: state database signed out or gone, keyring
// mirror still holding a valid login. Present and identified are different
// answers here — presence comes from the keyring, identity only from the state
// database, so a keyring-only machine is detected with no identity. That is the
// reference's behaviour too, and it is the safe one: fingerprinting would mean
// reading the secret.
func TestGeminiDetectLiveSeesKeyringMirrorWhenStateIsSignedOut(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	stubAntigravityKeyring(t, func(args ...string) (string, error) { return "", nil })

	p := newGemini(home)
	live, err := p.DetectLive()
	if err != nil {
		t.Fatal(err)
	}
	if !live.Present {
		t.Fatal("Antigravity keyring login not detected")
	}
	if live.Identity != "" {
		t.Fatalf("identity = %q, want empty: fingerprinting here would mean "+
			"reading the secret during a status poll", live.Identity)
	}
	if !strings.Contains(live.Details, "antigravity") {
		t.Fatalf("details = %q, want an antigravity explanation", live.Details)
	}
}

// The secret read belongs on the snapshot path, where the user asked for it.
func TestGeminiBackupFallsBackToTheOSKeyring(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	stubAntigravityKeyring(t, func(args ...string) (string, error) {
		if args[len(args)-1] != "-w" {
			return "", nil // the existence probe
		}
		return antigravityKeyringPrefix +
			base64.StdEncoding.EncodeToString([]byte(antigravityKeyringTestPayload)), nil
	})

	p := newGemini(home)
	dest := t.TempDir()
	info, err := p.Backup(dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, antigravityCredentialFiles[0])); err != nil {
		t.Fatalf("snapshot carries no Antigravity credential: %v", err)
	}
	if !strings.HasPrefix(info.Identity, "antigravity:") {
		t.Fatalf("identity = %q, want the antigravity grant hash", info.Identity)
	}
	if !strings.Contains(info.Notes, "keyring") {
		t.Fatalf("notes = %q, want the keyring source named", info.Notes)
	}
}
