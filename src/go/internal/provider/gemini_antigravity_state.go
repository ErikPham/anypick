package provider

// Antigravity keeps its authoritative OAuth login in the IDE's VS Code
// global-state SQLite database. The token is stored as a base64 protobuf topic
// under antigravityUnifiedStateSync.oauthToken. A portable copy under
// ~/.gemini is useful to AnyPick's proxy, but changing that copy alone does
// not change the account Antigravity itself renders or uses.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/db"
)

const (
	antigravityStateStorageKey = "antigravityUnifiedStateSync.oauthToken"
	antigravityTokenSentinel   = "oauthTokenInfoSentinelKey"
	antigravityAuthSentinel    = "authStateWithContextSentinelKey"
	antigravityUserStatusKey   = "antigravityUnifiedStateSync.userStatus"
	antigravityUserSentinel    = "userStatusSentinelKey"
)

var antigravityLegacyCacheKeys = []string{
	"antigravityAuthStatus",
	"antigravity.profileUrl",
}

// antigravityStateDatabasePaths lists known Antigravity user-data locations,
// newest product name first.
func antigravityStateDatabasePaths(home string) []string {
	if override := strings.TrimSpace(os.Getenv("ANTIGRAVITY_USER_DATA_DIR")); override != "" {
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
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{
			filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb"),
			filepath.Join(appData, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		}
	default:
		config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return []string{
			filepath.Join(config, "Antigravity", "User", "globalStorage", "state.vscdb"),
			filepath.Join(config, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		}
	}
}

func existingAntigravityStatePaths(home string) []string {
	paths := make([]string, 0, 2)
	seen := make(map[string]bool)
	for _, path := range antigravityStateDatabasePaths(home) {
		if seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		}
	}
	sort.SliceStable(paths, func(i, j int) bool {
		return modTimeOf(paths[i]).After(modTimeOf(paths[j]))
	})
	return paths
}

// antigravityStateCredential reads the live Antigravity login. It is
// deliberately best-effort for status polling: a missing database, an older
// schema, or a signed-out topic all mean "nothing here" rather than an error.
func antigravityStateCredential(home string) *geminiAntigravityFile {
	for _, path := range existingAntigravityStatePaths(home) {
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
	payload, err := readAntigravityStateFileStrict(path)
	return payload, err == nil && payload != nil
}

func readAntigravityStateFileStrict(path string) (*geminiAntigravityFile, error) {
	d, err := db.OpenForeign(path, true)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	topic, err := readAntigravityTopic(d, antigravityStateStorageKey)
	if err != nil {
		return nil, err
	}
	encoded, found := antigravityTopicEntry(topic, antigravityTokenSentinel)
	if !found || encoded == "" {
		return nil, nil
	}
	payload, ok := decodeAntigravityTokenInfo(encoded)
	if !ok {
		return nil, fmt.Errorf("invalid Antigravity OAuth token topic in %s", path)
	}
	return payload, nil
}

type antigravitySQL interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func readAntigravityTopic(q antigravitySQL, storageKey string) ([]byte, error) {
	var raw any
	if err := q.QueryRow("SELECT value FROM ItemTable WHERE key = ?", storageKey).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var encoded string
	switch value := raw.(type) {
	case []byte:
		encoded = string(value)
	case string:
		encoded = value
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported Antigravity state topic value %T", raw)
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}
	topic, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid Antigravity unified-state base64: %w", err)
	}
	if _, err := parseAntigravityProtoChecked(topic); err != nil {
		return nil, err
	}
	return topic, nil
}

func writeAntigravityTopic(q antigravitySQL, storageKey string, topic []byte) error {
	encoded := base64.StdEncoding.EncodeToString(topic)
	updated, err := q.Exec("UPDATE ItemTable SET value = ? WHERE key = ?", encoded, storageKey)
	if err != nil {
		return err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		_, err = q.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)", storageKey, encoded)
		return err
	}
	return nil
}

// antigravityTopicEntry walks topic → entry{1: sentinel, 2: row{1: encoded}}.
func antigravityTopicEntry(topic []byte, sentinel string) (string, bool) {
	for _, field := range parseAntigravityProto(topic) {
		if field.number != 1 || field.wireType != 2 {
			continue
		}
		entry := parseAntigravityProto(field.bytes)
		if firstAntigravityBytesField(entry, 1) != sentinel {
			continue
		}
		row := firstAntigravityBytesField(entry, 2)
		for _, inner := range parseAntigravityProto([]byte(row)) {
			if inner.number == 1 && inner.wireType == 2 {
				return string(inner.bytes), true
			}
		}
	}
	return "", false
}

func firstAntigravityBytesField(fields []antigravityProtoField, number int) string {
	for _, field := range fields {
		if field.number == number && field.wireType == 2 {
			return string(field.bytes)
		}
	}
	return ""
}

func replaceAntigravityBytesField(fields []antigravityProtoField, number int, value []byte) []antigravityProtoField {
	replaced := false
	next := make([]antigravityProtoField, 0, len(fields)+1)
	for _, field := range fields {
		if field.number != number {
			next = append(next, field)
			continue
		}
		if !replaced {
			next = append(next, antigravityProtoField{number: number, wireType: 2, bytes: value})
			replaced = true
		}
	}
	if !replaced {
		next = append(next, antigravityProtoField{number: number, wireType: 2, bytes: value})
	}
	return next
}

// rewriteAntigravityTopicEntry replaces or deletes one named topic entry while
// preserving unrelated protobuf fields. The state database contains other
// Antigravity state that AnyPick does not own.
func rewriteAntigravityTopicEntry(topic []byte, sentinel string, encodedValue *string) ([]byte, error) {
	fields, err := parseAntigravityProtoChecked(topic)
	if err != nil {
		return nil, err
	}
	found := false
	next := make([]antigravityProtoField, 0, len(fields)+1)
	for _, field := range fields {
		if field.number != 1 || field.wireType != 2 {
			next = append(next, field)
			continue
		}
		entry, entryErr := parseAntigravityProtoChecked(field.bytes)
		if entryErr != nil || firstAntigravityBytesField(entry, 1) != sentinel {
			next = append(next, field)
			continue
		}
		found = true
		if encodedValue == nil {
			continue
		}
		row := firstAntigravityBytesField(entry, 2)
		rowFields, rowErr := parseAntigravityProtoChecked([]byte(row))
		if rowErr != nil {
			return nil, rowErr
		}
		nextRow := encodeAntigravityProto(replaceAntigravityBytesField(
			rowFields, 1, []byte(*encodedValue)))
		nextEntry := encodeAntigravityProto(replaceAntigravityBytesField(entry, 2, nextRow))
		next = append(next, antigravityProtoField{number: 1, wireType: 2, bytes: nextEntry})
	}
	if !found && encodedValue != nil {
		row := encodeAntigravityProto([]antigravityProtoField{{
			number: 1, wireType: 2, bytes: []byte(*encodedValue),
		}})
		entry := encodeAntigravityProto([]antigravityProtoField{
			{number: 1, wireType: 2, bytes: []byte(sentinel)},
			{number: 2, wireType: 2, bytes: row},
		})
		next = append(next, antigravityProtoField{number: 1, wireType: 2, bytes: entry})
	}
	return encodeAntigravityProto(next), nil
}

// decodeAntigravityTokenInfo decodes OAuthTokenInfo:
// 1 = access_token, 2 = token_type, 3 = refresh_token, 4 = expiry Timestamp.
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

func encodeAntigravityTokenInfo(payload *geminiAntigravityFile) (string, error) {
	if payload == nil || strings.TrimSpace(payload.Token.RefreshToken) == "" {
		return "", fmt.Errorf("refusing to write Antigravity state without a refresh token")
	}
	tokenType := payload.Token.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	fields := []antigravityProtoField{
		{number: 1, wireType: 2, bytes: []byte(payload.Token.AccessToken)},
		{number: 2, wireType: 2, bytes: []byte(tokenType)},
		{number: 3, wireType: 2, bytes: []byte(payload.Token.RefreshToken)},
	}
	if strings.TrimSpace(payload.Token.Expiry) != "" {
		expiry, err := time.Parse(time.RFC3339Nano, payload.Token.Expiry)
		if err != nil {
			return "", fmt.Errorf("invalid Antigravity OAuth expiry: %w", err)
		}
		timestamp := []antigravityProtoField{{number: 1, wireType: 0, varint: uint64(expiry.Unix())}}
		if nanos := expiry.Nanosecond(); nanos != 0 {
			timestamp = append(timestamp, antigravityProtoField{number: 2, wireType: 0, varint: uint64(nanos)})
		}
		fields = append(fields, antigravityProtoField{
			number: 4, wireType: 2, bytes: encodeAntigravityProto(timestamp),
		})
	}
	return base64.StdEncoding.EncodeToString(encodeAntigravityProto(fields)), nil
}

// decodeAntigravityTimestamp reads google.protobuf.Timestamp.
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

// parseAntigravityProto supports every protobuf wire type that can occur in
// the VS Code topic. Keeping fixed-width fields is important: rewriting the
// OAuth entry must not drop unrelated future fields from the same topic.
func parseAntigravityProto(buf []byte) []antigravityProtoField {
	fields, _ := parseAntigravityProtoChecked(buf)
	return fields
}

func parseAntigravityProtoChecked(buf []byte) ([]antigravityProtoField, error) {
	var out []antigravityProtoField
	for i := 0; i < len(buf); {
		key, next := readAntigravityVarint(buf, i)
		if next < 0 || key>>3 == 0 {
			return nil, fmt.Errorf("invalid Antigravity protobuf key")
		}
		i = next
		field := antigravityProtoField{number: int(key >> 3), wireType: int(key & 7)}
		switch field.wireType {
		case 0:
			value, next := readAntigravityVarint(buf, i)
			if next < 0 {
				return nil, fmt.Errorf("truncated Antigravity protobuf varint")
			}
			field.varint, i = value, next
		case 1:
			if i+8 > len(buf) {
				return nil, fmt.Errorf("truncated Antigravity protobuf fixed64 field")
			}
			field.bytes, i = buf[i:i+8], i+8
		case 2:
			length, next := readAntigravityVarint(buf, i)
			if next < 0 || length > uint64(len(buf)-next) {
				return nil, fmt.Errorf("truncated Antigravity protobuf bytes field")
			}
			field.bytes, i = buf[next:next+int(length)], next+int(length)
		case 5:
			if i+4 > len(buf) {
				return nil, fmt.Errorf("truncated Antigravity protobuf fixed32 field")
			}
			field.bytes, i = buf[i:i+4], i+4
		default:
			return nil, fmt.Errorf("unsupported Antigravity protobuf wire type %d", field.wireType)
		}
		out = append(out, field)
	}
	return out, nil
}

func encodeAntigravityProto(fields []antigravityProtoField) []byte {
	var out []byte
	for _, field := range fields {
		out = append(out, encodeAntigravityVarint(uint64(field.number<<3|field.wireType))...)
		switch field.wireType {
		case 0:
			out = append(out, encodeAntigravityVarint(field.varint)...)
		case 1, 5:
			out = append(out, field.bytes...)
		case 2:
			out = append(out, encodeAntigravityVarint(uint64(len(field.bytes)))...)
			out = append(out, field.bytes...)
		}
	}
	return out
}

func encodeAntigravityVarint(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
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

// assertAntigravityStateSafeToMutate validates every installed state envelope
// before the Keychain is changed. A running IDE is not a reason to leave the
// user stuck on the old account: the state is updated and Antigravity will
// consume it on its next auth-state reload/startup.
func assertAntigravityStateSafeToMutate(home string) ([]string, error) {
	paths := existingAntigravityStatePaths(home)
	for _, path := range paths {
		if _, err := readAntigravityStateFileStrict(path); err != nil {
			return nil, fmt.Errorf("cannot validate Antigravity state %s: %w", path, err)
		}
		d, err := db.OpenForeign(path, true)
		if err != nil {
			return nil, err
		}
		_, err = readAntigravityTopic(d, antigravityUserStatusKey)
		_ = d.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot validate Antigravity user state %s: %w", path, err)
		}
	}
	return paths, nil
}

// writeAntigravityStateOAuthPayload updates every installed Antigravity state
// database and removes cached auth/profile entries that would otherwise keep
// the old email visible after the token has changed.
func writeAntigravityStateOAuthPayload(home string, payload *geminiAntigravityFile, paths []string) error {
	if len(paths) == 0 {
		var err error
		paths, err = assertAntigravityStateSafeToMutate(home)
		if err != nil {
			return err
		}
	}
	encoded, err := encodeAntigravityTokenInfo(payload)
	if err != nil {
		return err
	}
	for _, path := range paths {
		d, err := db.OpenForeign(path, false)
		if err != nil {
			return err
		}
		tx, err := d.Begin()
		if err != nil {
			_ = d.Close()
			return err
		}
		topic, err := readAntigravityTopic(tx, antigravityStateStorageKey)
		if err == nil {
			topic, err = clearAntigravityAccountCaches(tx, topic)
		}
		if err == nil {
			topic, err = rewriteAntigravityTopicEntry(topic, antigravityTokenSentinel, &encoded)
		}
		if err == nil {
			err = writeAntigravityTopic(tx, antigravityStateStorageKey, topic)
		}
		if err != nil {
			_ = tx.Rollback()
			_ = d.Close()
			return fmt.Errorf("write Antigravity state %s: %w", path, err)
		}
		if err := tx.Commit(); err != nil {
			_ = d.Close()
			return fmt.Errorf("commit Antigravity state %s: %w", path, err)
		}
		_ = d.Close()

		verified, readErr := readAntigravityStateFileStrict(path)
		if readErr != nil || verified == nil || verified.Token.RefreshToken != payload.Token.RefreshToken {
			if readErr == nil {
				readErr = fmt.Errorf("refresh token did not verify")
			}
			return fmt.Errorf("antigravity OAuth state verification failed for %s: %w", path, readErr)
		}
	}
	return nil
}

func clearAntigravityAccountCaches(q antigravitySQL, oauthTopic []byte) ([]byte, error) {
	withoutAuth, err := rewriteAntigravityTopicEntry(oauthTopic, antigravityAuthSentinel, nil)
	if err != nil {
		return nil, err
	}
	userTopic, err := readAntigravityTopic(q, antigravityUserStatusKey)
	if err != nil {
		return nil, err
	}
	if _, found := antigravityTopicEntry(userTopic, antigravityUserSentinel); found {
		cleared, clearErr := rewriteAntigravityTopicEntry(userTopic, antigravityUserSentinel, nil)
		if clearErr != nil {
			return nil, clearErr
		}
		if err := writeAntigravityTopic(q, antigravityUserStatusKey, cleared); err != nil {
			return nil, err
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(antigravityLegacyCacheKeys)), ",")
	args := make([]any, len(antigravityLegacyCacheKeys))
	for i, key := range antigravityLegacyCacheKeys {
		args[i] = key
	}
	if _, err := q.Exec("DELETE FROM ItemTable WHERE key IN ("+placeholders+")", args...); err != nil {
		return nil, err
	}
	return withoutAuth, nil
}

// antigravityIdentity identifies a login by its durable grant without
// exposing the refresh token. It is used only when Antigravity gives us no
// email in a portable mirror.
func antigravityIdentity(refreshToken string) string {
	if refreshToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(refreshToken))
	return "antigravity:" + hex.EncodeToString(sum[:])[:12]
}
