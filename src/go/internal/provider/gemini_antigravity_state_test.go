package provider

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/db"
)

// Antigravity's login never reaches ~/.gemini, so every test that only drops a
// file there proves nothing about the machines this code exists for. These
// build a real state.vscdb the way the IDE writes it — base64 topic, protobuf
// entries addressed by sentinel key — and pin the reader against it.

func antigravityVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func antigravityBytesField(number int, payload []byte) []byte {
	out := antigravityVarint(uint64(number)<<3 | 2)
	out = append(out, antigravityVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

// antigravityTokenInfo encodes the OAuthTokenInfo message:
// 1 access_token, 2 token_type, 3 refresh_token, 4 expiry (Timestamp).
func antigravityTokenInfo(access, tokenType, refresh string, expiry time.Time) []byte {
	var out []byte
	if access != "" {
		out = append(out, antigravityBytesField(1, []byte(access))...)
	}
	if tokenType != "" {
		out = append(out, antigravityBytesField(2, []byte(tokenType))...)
	}
	out = append(out, antigravityBytesField(3, []byte(refresh))...)
	if !expiry.IsZero() {
		seconds := uint64(expiry.Unix())
		// Timestamp{1: seconds}: field 1, wire type 0 (varint). The wire
		// type is written as a comment rather than `| 0` because the no-op
		// OR is exactly what staticcheck's SA4016 reports.
		stamp := antigravityVarint(1 << 3)
		stamp = append(stamp, antigravityVarint(seconds)...)
		out = append(out, antigravityBytesField(4, stamp)...)
	}
	return out
}

// antigravityTopic wraps one entry: {1: sentinel, 2: {1: base64(payload)}}.
func antigravityTopic(sentinel string, encoded []byte) []byte {
	row := antigravityBytesField(1, []byte(base64.StdEncoding.EncodeToString(encoded)))
	entry := antigravityBytesField(1, []byte(sentinel))
	entry = append(entry, antigravityBytesField(2, row)...)
	return antigravityBytesField(1, entry)
}

func writeAntigravityState(t *testing.T, home, sentinel string, encoded []byte) string {
	t.Helper()
	dir := filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.vscdb")
	// OpenForeign stats before opening (so a probe can never create a file in
	// another product's directory), so the test must lay the file down first.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.OpenForeign(path, false)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer d.Close()
	if _, err := d.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value BLOB)"); err != nil {
		t.Fatal(err)
	}
	value := base64.StdEncoding.EncodeToString(antigravityTopic(sentinel, encoded))
	if _, err := d.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)",
		antigravityStateStorageKey, value); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAntigravityStateCredentialReadsSignedInTopic(t *testing.T) {
	home := t.TempDir()
	expiry := time.Unix(1_800_000_000, 0).UTC()
	writeAntigravityState(t, home, antigravityTokenSentinel,
		antigravityTokenInfo("ya29.access", "Bearer", "1//refresh", expiry))

	cred := antigravityStateCredential(home)
	if cred == nil {
		t.Fatal("no credential read from a signed-in Antigravity state database")
	}
	if cred.Token.RefreshToken != "1//refresh" {
		t.Fatalf("refresh token = %q", cred.Token.RefreshToken)
	}
	if cred.Token.AccessToken != "ya29.access" {
		t.Fatalf("access token = %q", cred.Token.AccessToken)
	}
	if cred.Token.TokenType != "Bearer" {
		t.Fatalf("token type = %q", cred.Token.TokenType)
	}
	if cred.Token.Expiry != expiry.Format(time.RFC3339Nano) {
		t.Fatalf("expiry = %q, want %q", cred.Token.Expiry, expiry.Format(time.RFC3339Nano))
	}
}

// The same database without the token entry is what a signed-out IDE looks
// like: present, readable, and carrying nothing usable.
func TestAntigravityStateCredentialIgnoresOtherEntries(t *testing.T) {
	home := t.TempDir()
	writeAntigravityState(t, home, "authStateWithContextSentinelKey",
		[]byte(`{"state":"signedOut","context":{}}`))
	if cred := antigravityStateCredential(home); cred != nil {
		t.Fatalf("a signed-out topic produced a credential: %+v", cred.Token)
	}
}

// A refresh token is the whole point: without one the grant dies at the first
// access-token expiry, so the payload must read as absent.
func TestAntigravityStateCredentialNeedsARefreshToken(t *testing.T) {
	home := t.TempDir()
	writeAntigravityState(t, home, antigravityTokenSentinel,
		antigravityTokenInfo("ya29.access", "Bearer", "", time.Time{}))
	if cred := antigravityStateCredential(home); cred != nil {
		t.Fatalf("an access-only payload must not count: %+v", cred.Token)
	}
}

func TestAntigravityStateCredentialMissingDatabaseIsAbsent(t *testing.T) {
	if cred := antigravityStateCredential(t.TempDir()); cred != nil {
		t.Fatalf("empty home produced a credential: %+v", cred.Token)
	}
}

// End to end: a machine with only an Antigravity login (no ~/.gemini
// credential at all) must still be savable — this is the case that used to
// answer "No live Gemini CLI auth found".
func TestGeminiAddAccountFromAntigravityIDEState(t *testing.T) {
	t.Setenv("GEMINI_CONFIG_DIR", "")
	t.Setenv("ANTIGRAVITY_USER_DATA_DIR", "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAntigravityState(t, home, antigravityTokenSentinel,
		antigravityTokenInfo("ya29.access", "Bearer", "1//refresh", time.Now().Add(time.Hour)))

	p := newGemini(home)
	live, err := p.DetectLive()
	if err != nil {
		t.Fatal(err)
	}
	if !live.Present {
		t.Fatal("Antigravity IDE login not detected")
	}
	if !strings.HasPrefix(live.Identity, "antigravity:") {
		t.Fatalf("identity = %q, want the antigravity grant hash", live.Identity)
	}

	dest := t.TempDir()
	info, err := p.Backup(dest)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, antigravityCredentialFiles[0])); err != nil {
		t.Fatalf("snapshot carries no Antigravity credential: %v", err)
	}
	if info.Identity != live.Identity {
		t.Fatalf("backup identity = %q, want %q", info.Identity, live.Identity)
	}
}

// The credential must resolve to the Antigravity upstream once saved, or the
// account starts and answers nothing (the buildServer/TranslateGemini class of
// silent failure).
func TestGeminiUpstreamResolvesIDEStateSnapshot(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_SOURCE", "")
	dest := t.TempDir()
	raw := []byte(`{"token":{"access_token":"ya29.access","refresh_token":"1//refresh",` +
		`"token_type":"Bearer","expiry":"` + time.Now().Add(time.Hour).Format(time.RFC3339Nano) + `"}}`)
	if err := os.WriteFile(filepath.Join(dest, antigravityCredentialFiles[0]), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	up, err := geminiUpstream(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !up.TranslateGemini || up.Gemini == nil {
		t.Fatalf("snapshot resolved to a non-Gemini upstream: translate=%v target=%v",
			up.TranslateGemini, up.Gemini)
	}
	if up.Gemini.IDEType != "ANTIGRAVITY" || up.Gemini.UserAgent == "" {
		t.Fatalf("client identity = %+v", up.Gemini)
	}
}

func TestAntigravityIdentityIsStable(t *testing.T) {
	first := antigravityIdentity("1//refresh")
	if first == "" || !strings.HasPrefix(first, "antigravity:") {
		t.Fatalf("identity = %q", first)
	}
	if first != antigravityIdentity("1//refresh") {
		t.Fatal("identity must depend only on the grant")
	}
	if antigravityIdentity("") != "" {
		t.Fatal("an empty grant must not produce an identity")
	}
}

func TestAntigravityStateDatabasePathsCoverBothProducts(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin layout is what this machine ships")
	}
	paths := antigravityStateDatabasePaths("/home/x")
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want both Antigravity products", paths)
	}
	for _, p := range paths {
		if !strings.HasSuffix(filepath.ToSlash(p), "User/globalStorage/state.vscdb") {
			t.Fatalf("unexpected path %q", p)
		}
	}
}
