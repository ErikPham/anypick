package desktop

// Strict command validation mirroring isTray*Command guards in
// src/tray/protocol.ts: unknown fields rejected, required fields present,
// booleans explicit where TS demands them, bounded strings, unique bounded
// entryIds. A payload that TypeScript would reject must not reach a handler.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodeStrict unmarshals with unknown-field rejection.
func decodeStrict(raw []byte, into interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("payload rejected: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("payload rejected: trailing JSON value")
		}
		return fmt.Errorf("payload rejected: %w", err)
	}
	return nil
}

func safeStr(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func safeOptional(s string, max int) bool {
	if s == "" {
		return true
	}
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func safePath(p string) bool {
	if p == "" || len(p) > 4096 {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func hasKey(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

func optionalStringField(fields map[string]json.RawMessage, key string, max int) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", nil
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("invalid %s", key)
	}
	text, ok := value.(string)
	if !ok || !safeOptional(text, max) {
		return "", fmt.Errorf("invalid %s", key)
	}
	return text, nil
}

func boolField(fields map[string]json.RawMessage, key string) error {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s must be boolean", key)
	}
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("%s must be boolean", key)
	}
	return nil
}

func keysSubset(fields map[string]json.RawMessage, allowed ...string) bool {
	set := map[string]bool{}
	for _, k := range allowed {
		set[k] = true
	}
	for k := range fields {
		if !set[k] {
			return false
		}
	}
	return true
}

var mutationOperations = map[string]bool{
	"account-detect": true, "account-import": true, "account-export": true,
	"account-backup-all": true, "account-backup-export": true, "account-backup-import": true,
	"account-login": true, "account-save-current": true, "account-save": true,
	"account-edit": true, "account-refresh": true, "account-remove": true,
	"account-clear": true, "gateway-create": true, "gateway-test": true,
	"gateway-edit": true, "gateway-refresh": true, "gateway-remove": true,
	"client-reset": true, "hub-source-toggle": true,
	"proxy-restart-all": true, "proxy-stop-all": true,
	"setting-launch-at-login": true, "setting-auto-start-proxies": true,
	"setting-show-quota": true, "setting-quota-guard": true,
}

// validatePayload enforces the per-kind contracts after a strict decode.
func validatePayload(kind string, raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	version := 0
	_ = json.Unmarshal(fields["version"], &version)
	if version != ProtocolVersion {
		return fmt.Errorf("unsupported version %d", version)
	}
	requestID := ""
	_ = json.Unmarshal(fields["requestId"], &requestID)
	if !safeStr(requestID, 128) {
		return fmt.Errorf("invalid requestId")
	}

	switch kind {
	case "invoke":
		actionID := ""
		_ = json.Unmarshal(fields["actionId"], &actionID)
		if !safeStr(actionID, 256) {
			return fmt.Errorf("invalid actionId")
		}
	case "model-roles":
		clientID := ""
		_ = json.Unmarshal(fields["clientId"], &clientID)
		if !safeStr(clientID, 64) {
			return fmt.Errorf("invalid clientId")
		}
		roles := map[string]string{}
		if raw, ok := fields["roleActionIds"]; ok {
			if json.Unmarshal(raw, &roles) != nil {
				return fmt.Errorf("invalid roleActionIds")
			}
			for role, model := range roles {
				if !safeStr(role, 64) || !safeOptional(model, 256) {
					return fmt.Errorf("invalid roleActionIds entry")
				}
			}
		}
	case "logs":
		providerID, name := "", ""
		lines := -1
		_ = json.Unmarshal(fields["providerId"], &providerID)
		_ = json.Unmarshal(fields["name"], &name)
		_ = json.Unmarshal(fields["lines"], &lines)
		if !safeStr(providerID, 64) || !safeStr(name, 128) ||
			lines < 1 || lines > 200 {
			return fmt.Errorf("invalid logs request")
		}
	case "backup-inspect":
		path := ""
		_ = json.Unmarshal(fields["path"], &path)
		if !keysSubset(fields, "version", "requestId", "path") || !safePath(path) {
			return fmt.Errorf("invalid backup-inspect request")
		}
	case "cancel":
		// requestId only; already validated.
	case "mutate":
		return validateMutate(fields)
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	return nil
}

func validateMutate(fields map[string]json.RawMessage) error {
	operation := ""
	_ = json.Unmarshal(fields["operation"], &operation)
	if !mutationOperations[operation] {
		return fmt.Errorf("unknown mutation operation")
	}
	if path, ok := fields["path"]; ok {
		var p string
		_ = json.Unmarshal(path, &p)
		if !safePath(p) && !(operation == "account-backup-all") {
			return fmt.Errorf("invalid path")
		}
	}
	for key, limit := range map[string]int{
		"providerId": 64, "sourceId": 64, "methodId": 128, "name": 128,
		"label": 160, "endpoint": 2048, "apiKey": 8192, "region": 64,
		"defaultModel": 256,
	} {
		if _, err := optionalStringField(fields, key, limit); err != nil {
			return err
		}
	}
	for _, key := range []string{"overwrite", "enabled", "overwriteConfirmed"} {
		if err := boolField(fields, key); err != nil {
			return err
		}
	}

	switch operation {
	case "account-backup-export":
		path := ""
		_ = json.Unmarshal(fields["path"], &path)
		if !hasKey(fields, "overwrite") || !hasKey(fields, "accounts") || !safePath(path) ||
			!keysSubset(fields, "version", "requestId", "operation", "path", "accounts", "overwrite") {
			return fmt.Errorf("invalid account-backup-export payload")
		}
		var accounts []BackupAccountRef
		_ = json.Unmarshal(fields["accounts"], &accounts)
		seen := map[string]bool{}
		if len(accounts) == 0 || len(accounts) > MaxBackupAccountRefs {
			return fmt.Errorf("accounts selection invalid")
		}
		for _, ref := range accounts {
			if !safeStr(ref.Provider, 64) || !safeStr(ref.Name, 128) {
				return fmt.Errorf("invalid account reference")
			}
			key := ref.Provider + "\x00" + ref.Name
			if seen[key] {
				return fmt.Errorf("duplicate account reference")
			}
			seen[key] = true
		}
	case "account-backup-import":
		inspectionID := ""
		_ = json.Unmarshal(fields["inspectionId"], &inspectionID)
		var entryIDs []string
		_ = json.Unmarshal(fields["entryIds"], &entryIDs)
		if !safeStr(inspectionID, 128) ||
			// TS authority: isUniqueSafeStrings(entryIds, 64, 16) — at most 64
			// entries, each at most 16 characters (src/tray/protocol.ts).
			len(entryIDs) == 0 || len(entryIDs) > 64 ||
			!hasKey(fields, "overwriteConfirmed") ||
			!keysSubset(fields, "version", "requestId", "operation",
				"inspectionId", "entryIds", "overwriteConfirmed") {
			return fmt.Errorf("invalid account-backup-import payload")
		}
		seen := map[string]bool{}
		for _, id := range entryIDs {
			if !safeStr(id, 16) || seen[id] {
				return fmt.Errorf("entryIds must be unique bounded strings")
			}
			seen[id] = true
		}
	case "account-backup-all":
		path := ""
		_ = json.Unmarshal(fields["path"], &path)
		if !safePath(path) || !keysSubset(fields, "version", "requestId", "operation", "path") {
			return fmt.Errorf("invalid account-backup-all payload")
		}
	case "account-import", "account-export":
		providerID, name, path := "", "", ""
		_ = json.Unmarshal(fields["providerId"], &providerID)
		_ = json.Unmarshal(fields["name"], &name)
		_ = json.Unmarshal(fields["path"], &path)
		allowed := []string{"version", "requestId", "operation", "providerId", "name", "path"}
		if operation == "account-import" {
			allowed = append(allowed, "overwrite")
		}
		if !safeStr(providerID, 64) || !safeStr(name, 128) || !safePath(path) ||
			!keysSubset(fields, allowed...) {
			return fmt.Errorf("invalid %s payload", operation)
		}
	case "account-login":
		providerID := ""
		methodID := ""
		_ = json.Unmarshal(fields["providerId"], &providerID)
		_ = json.Unmarshal(fields["methodId"], &methodID)
		sourceID, loginName, loginLabel := "", "", ""
		_ = json.Unmarshal(fields["sourceId"], &sourceID)
		_ = json.Unmarshal(fields["name"], &loginName)
		_ = json.Unmarshal(fields["label"], &loginLabel)
		if !safeStr(providerID, 64) || !safeStr(methodID, 128) ||
			(hasKey(fields, "sourceId") && !safeStr(sourceID, 64)) ||
			(hasKey(fields, "name") && !safeStr(loginName, 128)) ||
			!safeOptional(loginLabel, 160) ||
			!keysSubset(fields, "version", "requestId", "operation",
				"providerId", "sourceId", "methodId", "name", "label") {
			return fmt.Errorf("invalid account-login payload")
		}
	case "account-save-current":
		providerID, sourceID, name := "", "", ""
		_ = json.Unmarshal(fields["providerId"], &providerID)
		_ = json.Unmarshal(fields["sourceId"], &sourceID)
		_ = json.Unmarshal(fields["name"], &name)
		if !safeStr(providerID, 64) ||
			(hasKey(fields, "sourceId") && !safeStr(sourceID, 64)) ||
			(hasKey(fields, "name") && !safeStr(name, 128)) ||
			!keysSubset(fields, "version", "requestId", "operation", "providerId", "sourceId", "name") {
			return fmt.Errorf("invalid account-save-current payload")
		}
	default:
		name := ""
		_ = json.Unmarshal(fields["name"], &name)
		if !safeStr(name, 128) {
			return fmt.Errorf("invalid name")
		}
		if _, hasPath := fields["path"]; hasPath {
			return fmt.Errorf("%s must not carry a path", operation)
		}
		needsProvider := map[string]bool{
			"account-detect": true, "account-save": true, "account-edit": true,
			"account-refresh": true, "account-remove": true, "account-clear": true,
			"gateway-create": true, "gateway-test": true, "hub-source-toggle": true,
		}[operation]
		if needsProvider {
			providerID := ""
			_ = json.Unmarshal(fields["providerId"], &providerID)
			if !safeStr(providerID, 64) {
				return fmt.Errorf("invalid providerId")
			}
		}
		isSetting := len(operation) > 8 && operation[:8] == "setting-"
		if isSetting || operation == "hub-source-toggle" {
			if !hasKey(fields, "enabled") {
				return fmt.Errorf("%s requires enabled", operation)
			}
		}
	}
	return nil
}

// MaxBackupAccountRefs mirrors the sane upper bound on one export selection.
// MaxBackupAccountRefs mirrors isBackupAccountRefs in src/tray/protocol.ts:
// a desktop backup-export selection carries at most 64 accounts.
const MaxBackupAccountRefs = 64
