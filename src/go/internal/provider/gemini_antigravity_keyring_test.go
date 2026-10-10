package provider

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the real OS credential store and Antigravity's per-user
// state out of the test process.
//
// Antigravity's keychain item is machine-global: unlike every other credential
// this package reads, it is not scoped to a home directory a test can point at
// a temp dir. Left reachable, `DetectLive` would report a Gemini login as
// present on a developer's machine that happens to be signed in to Antigravity,
// and absent in CI — a suite whose result depends on who runs it, which is the
// worst possible kind of test. Tests that mean to exercise the keyring opt back
// in by replacing the hook themselves.
func TestMain(m *testing.M) {
	// Linux and Windows put Antigravity state under XDG_CONFIG_HOME and APPDATA,
	// which can point outside the fake HOME passed to a provider. Clearing these
	// makes the production path resolver fall back to that provider's home.
	for _, key := range []string{
		"XDG_CONFIG_HOME", "APPDATA", "ANTIGRAVITY_USER_DATA_DIR",
		"GEMINI_CONFIG_DIR", "GEMINI_CLI_HOME", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "GEMINI_OAUTH_SOURCE",
	} {
		_ = os.Setenv(key, "")
	}
	antigravityKeyringExec = func(args ...string) (string, error) {
		return "", exec.ErrNotFound
	}
	AntigravityKeyringProbe = func() bool { return false }
	AntigravityKeyringRead = func() *geminiAntigravityFile { return nil }
	// Restore tests use a fake provider environment; never mutate the
	// developer's real Keychain from the package test binary.
	AntigravityKeyringPut = func(*geminiAntigravityFile) error { return nil }
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

func stubAntigravityKeyringPut(t *testing.T, fn func(*geminiAntigravityFile) error) {
	t.Helper()
	AntigravityKeyringPut = fn
	t.Cleanup(func() {
		AntigravityKeyringPut = func(*geminiAntigravityFile) error { return nil }
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
	// DetectLive reads presence through the seam, not the exec var directly;
	// point it at a present-but-secretless fake so the detection path is what
	// is under test.
	previousProbe := AntigravityKeyringProbe
	AntigravityKeyringProbe = func() bool { return true }
	t.Cleanup(func() { AntigravityKeyringProbe = previousProbe })

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

func TestGeminiBackupAddsKeyringAsAlternateToGeminiCLI(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), []byte(
		`{"access_token":"cli-access","refresh_token":"cli-refresh","token_type":"Bearer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stubAntigravityKeyring(t, func(args ...string) (string, error) {
		if args[len(args)-1] != "-w" {
			return "", nil
		}
		return antigravityKeyringPrefix +
			base64.StdEncoding.EncodeToString([]byte(antigravityKeyringTestPayload)), nil
	})

	dest := t.TempDir()
	if _, err := newGemini(home).Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "oauth_creds.json")); err != nil {
		t.Fatalf("Gemini CLI credential missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "antigravity_oauth.json")); err != nil {
		t.Fatalf("Antigravity fallback credential missing: %v", err)
	}
	up, err := geminiUpstream(dest)
	if err != nil {
		t.Fatalf("geminiUpstream: %v", err)
	}
	if up.Gemini == nil || up.Gemini.AltRoute == nil {
		t.Fatal("both live sources must configure OAuth fallback")
	}
}

// The envelope a refreshed Antigravity login actually writes: access token
// plus an id_token. Hex-encoding it for `security -i -X` is ~4.5KB, past the
// tool's line buffer, so the write exits 1 on an unlocked keychain and the
// rollback write fails the same way. `-w` of the same value fits.
func TestAntigravityKeyringWriteKeepsAnIDTokenOffTheLineLimit(t *testing.T) {
	payload := &geminiAntigravityFile{}
	payload.Token.AccessToken = strings.Repeat("a", 260)
	payload.Token.RefreshToken = strings.Repeat("r", 103)
	payload.Token.TokenType = "Bearer"
	payload.Token.Expiry = "2026-09-29T15:25:18.88+07:00"
	payload.Email = "user@example.com"
	payload.IDToken = strings.Repeat("i", 1120)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	value := antigravityKeyringPrefix + base64.StdEncoding.EncodeToString(raw)
	stdin, args := antigravityDarwinInvocation(value)
	if stdin == "" || args != nil {
		t.Fatal("id_token envelope took the argv path; stdin -w still fits")
	}
	if len(stdin) > securityStdinLimit {
		t.Fatalf("stdin command is %d bytes, limit %d", len(stdin), securityStdinLimit)
	}
	if strings.Contains(stdin, " -X ") {
		t.Fatal("stdin command uses -X, which hex-doubles the envelope past the line buffer")
	}
	if !strings.Contains(stdin, " -w "+value) {
		t.Fatal("stdin command is not the go-keyring -w envelope")
	}
	hexForm := "add-generic-password -U -s \"gemini\" -a \"antigravity\" -X " +
		hex.EncodeToString([]byte(value)) + "\n"
	if len(hexForm) <= securityStdinLimit {
		t.Fatalf("fixture is %d bytes as -X, want it past the limit so the regression stays real", len(hexForm))
	}
}

func TestAntigravityKeyringWriteUsesArgvWhenStdinWouldTruncate(t *testing.T) {
	value := antigravityKeyringPrefix + strings.Repeat("A", securityStdinLimit)
	stdin, args := antigravityDarwinInvocation(value)
	if stdin != "" {
		t.Fatalf("oversized value stayed on stdin (%d bytes)", len(stdin))
	}
	if len(args) < 2 || args[len(args)-2] != "-X" {
		t.Fatalf("argv = %v, want a trailing -X", args)
	}
	decoded, err := hex.DecodeString(args[len(args)-1])
	if err != nil || string(decoded) != value {
		t.Fatalf("argv hex did not round-trip: %v", err)
	}
	for _, arg := range args[:len(args)-1] {
		if strings.Contains(arg, value[:24]) {
			t.Fatal("plaintext envelope leaked into argv")
		}
	}
}

func TestSecurityFailureHidesTruncatedSecret(t *testing.T) {
	err := securityFailure(
		os.ErrClosed,
		[]byte("security: unknown command \"go-keyring-base64:SECRET\""))
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), antigravityKeyringPrefix) {
		t.Fatalf("truncated secret leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("error = %v, want the truncation reason", err)
	}
}
