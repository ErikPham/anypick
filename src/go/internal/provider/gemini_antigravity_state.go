package provider

// Antigravity keeps its login in the IDE's own state database, not under
// ~/.gemini: the credential is mirrored into a VS Code global-state SQLite
// file as a base64 protobuf "topic". Without this reader an Antigravity login
// is invisible to AnyPick unless some build happened to drop a portable copy
// of the token under ~/.gemini — which is exactly how `add account gemini`
// failed on a machine that was plainly signed in.
//
// Ported read-only from the reference implementation (npm anypick,
// src/providers/gemini-antigravity-state.ts): readTopic + topicEntry +
// decodeOAuthTokenInfo. Nothing here writes, so no store can be corrupted by
// a probe. Write-back is deliberately not ported: the reference only mutates
// those databases after asserting the IDE is fully stopped, and a half-port of
// that safety check is worse than a read-only import.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/outof0/anypick/go/internal/db"
)

const (
	antigravityStateStorageKey = "antigravityUnifiedStateSync.oauthToken"
	antigravityTokenSentinel   = "oauthTokenInfoSentinelKey"
)

// antigravityStateDatabasePaths lists the known Antigravity user-data
// locations, newest product name first.
func antigravityStateDatabasePaths(home string) []string {
	if override := os.Getenv("ANTIGRAVITY_USER_DATA_DIR"); override != "" {
		return []string{filepath.Join(override, "User", "globalStorage", "state.vscdb")}
	}
	switch runtime.GOOS {
	case "darwin":
		support := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(support, "Antigravity", "User", "globalStorage", "state.vscdb"),
			filepath.Join(support, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{
			filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb"),
			filepath.Join(appData, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		}
	default:
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return []string{
			filepath.Join(config, "Antigravity", "User", "globalStorage", "state.vscdb"),
			filepath.Join(config, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		}
	}
}

// antigravityStateCredential reads the live Antigravity login. It returns nil
// when no signed-in credential exists: a missing database, an older schema or
// a signed-out topic are all "nothing here", never an error — the caller is
// deciding whether an account can be saved, and one unreadable IDE file must
// not stop a .env or gemini-cli login from being detected.
func antigravityStateCredential(home string) *geminiAntigravityFile {
	paths := antigravityStateDatabasePaths(home)
	// Prefer the product the user touched most recently (the reference sorts
	// by mtime desc): two installed builds can disagree, and the fresher one
	// is the login actually in use.
	sort.SliceStable(paths, func(i, j int) bool {
		return modTimeOf(paths[i]).After(modTimeOf(paths[j]))
	})
	for _, path := range paths {
		if payload, ok := readAntigravityStateFile(path); ok {
			return payload
		}
	}
	return nil
}

func modTimeOf(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func readAntigravityStateFile(path string) (*geminiAntigravityFile, bool) {
	d, err := db.OpenForeign(path, true)
	if err != nil {
		return nil, false
	}
	defer d.Close()
	var raw []byte
	if err := d.QueryRow("SELECT value FROM ItemTable WHERE key = ?",
		antigravityStateStorageKey).Scan(&raw); err != nil || len(raw) == 0 {
		return nil, false
	}
	topic, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, false
	}
	encoded, found := antigravityTopicEntry(topic, antigravityTokenSentinel)
	if !found {
		return nil, false
	}
	return decodeAntigravityTokenInfo(encoded)
}

// antigravityTopicEntry walks topic → entry{1: sentinel, 2: row{1: encoded}}.
func antigravityTopicEntry(topic []byte, sentinel string) (string, bool) {
	for _, field := range parseAntigravityProto(topic) {
		if field.number != 1 || field.wireType != 2 {
			continue
		}
		entry := parseAntigravityProto(field.bytes)
		var name string
		for _, f := range entry {
			if f.number == 1 && f.wireType == 2 {
				name = string(f.bytes)
				break
			}
		}
		if name != sentinel {
			continue
		}
		for _, f := range entry {
			if f.number == 2 && f.wireType == 2 {
				for _, inner := range parseAntigravityProto(f.bytes) {
					if inner.number == 1 && inner.wireType == 2 {
						return string(inner.bytes), true
					}
				}
			}
		}
	}
	return "", false
}

// decodeAntigravityTokenInfo decodes the OAuthTokenInfo message:
// 1 = access_token, 2 = token_type, 3 = refresh_token, 4 = expiry Timestamp.
// A payload without a refresh token is useless — it cannot be refreshed once
// the access token expires — so it is reported as absent.
func decodeAntigravityTokenInfo(encoded string) (*geminiAntigravityFile, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	payload := &geminiAntigravityFile{}
	for _, field := range parseAntigravityProto(raw) {
		if field.wireType != 2 {
			continue
		}
		switch field.number {
		case 1:
			payload.Token.AccessToken = string(field.bytes)
		case 2:
			payload.Token.TokenType = string(field.bytes)
		case 3:
			payload.Token.RefreshToken = string(field.bytes)
		case 4:
			payload.Token.Expiry = decodeAntigravityTimestamp(field.bytes)
		}
	}
	if payload.Token.RefreshToken == "" {
		return nil, false
	}
	if payload.Token.TokenType == "" {
		payload.Token.TokenType = "Bearer"
	}
	return payload, true
}

// decodeAntigravityTimestamp reads google.protobuf.Timestamp
// (1 = seconds, 2 = nanos) into the RFC3339 string the nested envelope uses.
func decodeAntigravityTimestamp(buf []byte) string {
	var seconds, nanos int64
	haveSeconds := false
	for _, field := range parseAntigravityProto(buf) {
		if field.wireType != 0 {
			continue
		}
		switch field.number {
		case 1:
			seconds, haveSeconds = int64(field.varint), true
		case 2:
			nanos = int64(field.varint)
		}
	}
	if !haveSeconds {
		return ""
	}
	return time.Unix(seconds, nanos).UTC().Format(time.RFC3339Nano)
}

type antigravityProtoField struct {
	number   int
	wireType int
	bytes    []byte
	varint   uint64
}

// parseAntigravityProto is a deliberately minimal reader for the two wire
// types Antigravity emits (0 varint, 2 length-delimited). It stops at the
// first field it cannot decode: a truncated or foreign topic yields the fields
// parsed so far rather than a panic or a bogus payload.
func parseAntigravityProto(buf []byte) []antigravityProtoField {
	var out []antigravityProtoField
	for i := 0; i < len(buf); {
		key, next := readAntigravityVarint(buf, i)
		if next < 0 {
			return out
		}
		i = next
		field := antigravityProtoField{number: int(key >> 3), wireType: int(key & 7)}
		switch field.wireType {
		case 0:
			value, next := readAntigravityVarint(buf, i)
			if next < 0 {
				return out
			}
			field.varint, i = value, next
		case 2:
			length, next := readAntigravityVarint(buf, i)
			if next < 0 || int(length) < 0 || next+int(length) > len(buf) {
				return out
			}
			field.bytes, i = buf[next:next+int(length)], next+int(length)
		default:
			return out
		}
		out = append(out, field)
	}
	return out
}

func readAntigravityVarint(buf []byte, offset int) (uint64, int) {
	var value uint64
	var shift uint
	for i := offset; i < len(buf) && i < offset+10; i++ {
		value |= uint64(buf[i]&0x7f) << shift
		if buf[i]&0x80 == 0 {
			return value, i + 1
		}
		shift += 7
	}
	return 0, -1
}

// antigravityIdentity identifies a login by its durable grant: Antigravity
// exposes no email, so the reference hashes the refresh token.
func antigravityIdentity(refreshToken string) string {
	if refreshToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(refreshToken))
	return "antigravity:" + hex.EncodeToString(sum[:])[:12]
}
