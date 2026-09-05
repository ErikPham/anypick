// Package tray ports the tray supervisor protocol (src/tray/protocol.ts,
// supervisor.ts): the core speaks line-framed base64 JSON over stdio —
//
//	core → app : snapshot\t<b64> | result\t<b64> | logs\t<b64> | backup-preview\t<b64>
//	app → core : open|refresh|restart|stop|quit | navigate\t<s>
//	             | invoke\t<b64> | mutate\t<b64> | model-roles\t<b64>
//	             | logs\t<b64> | backup-inspect\t<b64> | cancel\t<b64>
package desktop

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ProtocolVersion mirrors TRAY_PROTOCOL_VERSION.
const ProtocolVersion = 1

// MaxCommandBytes mirrors MAX_TRAY_COMMAND_BYTES.
const MaxCommandBytes = 16 * 1024

// Command is the decoded form of one app→core line.
type Command struct {
	Kind    string          // open|refresh|restart|stop|quit|navigate|invoke|model-roles|logs|backup-inspect|cancel|mutate
	Screen  string          // navigate target
	Payload json.RawMessage // decoded JSON body for prefixed kinds
}

type invokePayload struct {
	Version   int    `json:"version"`
	RequestID string `json:"requestId"`
	Revision  int    `json:"revision"`
	ActionID  string `json:"actionId"`
}

type modelRolesPayload struct {
	Version       int               `json:"version"`
	RequestID     string            `json:"requestId"`
	Revision      int               `json:"revision"`
	ClientID      string            `json:"clientId"`
	RoleActionIDs map[string]string `json:"roleActionIds"`
}

type logsPayload struct {
	Version    int    `json:"version"`
	RequestID  string `json:"requestId"`
	ProviderID string `json:"providerId"`
	Name       string `json:"name"`
	Lines      int    `json:"lines"`
}

type backupInspectPayload struct {
	Version   int    `json:"version"`
	RequestID string `json:"requestId"`
	Path      string `json:"path"`
}

type cancelPayload struct {
	Version   int    `json:"version"`
	RequestID string `json:"requestId"`
}

// Mutate mirrors TrayMutationCommand loosely: unknown fields are dropped, so
// new operations surface as unsupported instead of crashing the supervisor.
type Mutate struct {
	Version      int    `json:"version"`
	RequestID    string `json:"requestId"`
	Operation    string `json:"operation"`
	ProviderID   string `json:"providerId"`
	SourceID     string `json:"sourceId"`
	MethodID     string `json:"methodId"`
	Label        string `json:"label"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Region       string `json:"region"`
	DefaultModel string `json:"defaultModel"`
	Overwrite    bool   `json:"overwrite"`
	Enabled      bool   `json:"enabled"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	// account-save-input carries the credential product the user picked in
	// the native Add Account sheet. The secret itself travels in APIKey —
	// the same field gateway-create already uses for profile secrets — so
	// no second secret-shaped field is introduced.
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
	// Backup-package operations (TrayBackupAccountRef / import flow).
	Accounts           []BackupAccountRef `json:"accounts,omitempty"`
	InspectionID       string             `json:"inspectionId,omitempty"`
	EntryIDs           []string           `json:"entryIds,omitempty"`
	OverwriteConfirmed bool               `json:"overwriteConfirmed,omitempty"`
}

// BackupAccountRef mirrors TrayBackupAccountRef.
type BackupAccountRef struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

var simpleKinds = map[string]bool{
	"open": true, "refresh": true, "restart": true, "stop": true, "quit": true,
}

var navigateScreens = map[string]bool{
	"accounts": true, "gateways": true, "proxy": true, "proxy-hub": true,
	"add-account": true, "add-gateway": true,
}

// DecodeLine ports decodeTrayCommand. Malformed input returns an error the
// caller ignores — a bad line must never disrupt supervision.
func DecodeLine(line string) (*Command, error) {
	if len(line) > MaxCommandBytes {
		return nil, fmt.Errorf("command too large")
	}
	if simpleKinds[line] {
		return &Command{Kind: line}, nil
	}
	const (
		invokeP   = "invoke\x09"
		rolesP    = "model-roles\x09"
		logsP     = "logs\x09"
		inspectP  = "backup-inspect\x09"
		cancelP   = "cancel\x09"
		mutateP   = "mutate\x09"
		navigateP = "navigate\x09"
	)
	switch {
	case strings.HasPrefix(line, navigateP):
		screen := strings.TrimPrefix(line, navigateP)
		if !navigateScreens[screen] {
			return nil, fmt.Errorf("unknown screen %q", screen)
		}
		return &Command{Kind: "navigate", Screen: screen}, nil
	case strings.HasPrefix(line, invokeP):
		var p invokePayload
		raw, err := decodeStrictPayload(strings.TrimPrefix(line, invokeP), "invoke", &p)
		if err != nil {
			return nil, err
		}
		return &Command{Kind: "invoke", Payload: raw}, nil
	case strings.HasPrefix(line, rolesP):
		var p modelRolesPayload
		raw, err := decodeStrictPayload(strings.TrimPrefix(line, rolesP), "model-roles", &p)
		if err != nil {
			return nil, err
		}
		return &Command{Kind: "model-roles", Payload: raw}, nil
	case strings.HasPrefix(line, logsP):
		var p logsPayload
		raw, err := decodeStrictPayload(strings.TrimPrefix(line, logsP), "logs", &p)
		if err != nil {
			return nil, err
		}
		return &Command{Kind: "logs", Payload: raw}, nil
	case strings.HasPrefix(line, inspectP):
		var p backupInspectPayload
		raw, err := decodeStrictPayload(strings.TrimPrefix(line, inspectP), "backup-inspect", &p)
		if err != nil {
			return nil, err
		}
		return &Command{Kind: "backup-inspect", Payload: raw}, nil
	case strings.HasPrefix(line, cancelP):
		var p cancelPayload
		raw, err := decodeStrictPayload(strings.TrimPrefix(line, cancelP), "cancel", &p)
		if err != nil {
			return nil, err
		}
		return &Command{Kind: "cancel", Payload: raw}, nil
	case strings.HasPrefix(line, mutateP):
		var p Mutate
		raw, err := decodeStrictPayload(strings.TrimPrefix(line, mutateP), "mutate", &p)
		if err != nil {
			return nil, err
		}
		return &Command{Kind: "mutate", Payload: raw}, nil
	default:
		return nil, fmt.Errorf("unrecognized line")
	}
}

func decodeB64Body(encoded string, into interface{}) error {
	if encoded == "" || len(encoded) > MaxCommandBytes {
		return fmt.Errorf("bad payload length")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("bad base64: %w", err)
	}
	return json.Unmarshal(raw, into)
}

// ---- core → app emitters ----

func frame(prefix string, v interface{}) string {
	raw, _ := json.Marshal(v)
	return prefix + "\t" + base64.StdEncoding.EncodeToString(raw)
}

// FrameSnapshot mirrors sendSnapshot.
func FrameSnapshot(s *Snapshot) string { return frame("snapshot", s) }

// Result mirrors TrayCommandResult.
type Result struct {
	Version   int             `json:"version"`
	RequestID string          `json:"requestId"`
	Status    string          `json:"status"` // success | error | cancelled
	Message   string          `json:"message,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// FrameResult mirrors sendResult.
func FrameResult(r Result) string { return frame("result", r) }

// ProxyLogsResult mirrors TrayProxyLogsResult.
type ProxyLogsResult struct {
	Version   int    `json:"version"`
	RequestID string `json:"requestId"`
	ProxyID   string `json:"proxyId"`
	State     string `json:"state"` // ready | empty | not-running | error
	Text      string `json:"text"`
}

// FrameProxyLogs mirrors sendProxyLogs.
func FrameProxyLogs(r ProxyLogsResult) string { return frame("logs", r) }

// BackupPreviewEntry mirrors TrayBackupPreviewEntry. ID is the base-36 index
// TS assigns so entryIds from the native client resolve identically.
type BackupPreviewEntry struct {
	ID          string `json:"id"`
	ProviderID  string `json:"providerId"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

// BackupPreviewResult mirrors TrayBackupPreviewResult (ready | error).
type BackupPreviewResult struct {
	Version      int                  `json:"version"`
	RequestID    string               `json:"requestId"`
	Status       string               `json:"status"` // ready | error
	InspectionID string               `json:"inspectionId,omitempty"`
	CreatedAt    string               `json:"createdAt,omitempty"`
	Entries      []BackupPreviewEntry `json:"entries,omitempty"`
	Error        string               `json:"error,omitempty"`
	Code         string               `json:"code,omitempty"`
	Reason       string               `json:"reason,omitempty"`
	Message      string               `json:"message,omitempty"`
}

// FrameBackupPreview mirrors sendBackupPreview.
func FrameBackupPreview(r BackupPreviewResult) string { return frame("backup-preview", r) }

// decodeStrictPayload base64-decodes a payload, strictly unmarshals it, and
// applies the per-kind contract validation.
func decodeStrictPayload(encoded, kind string, into interface{}) (json.RawMessage, error) {
	if encoded == "" || len(encoded) > MaxCommandBytes {
		return nil, fmt.Errorf("bad payload length")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("bad base64: %w", err)
	}
	if err := decodeStrict(raw, into); err != nil {
		return nil, err
	}
	// Validation reads the ORIGINAL payload so explicitly-sent `false`
	// booleans count as present, matching the TS key-presence checks.
	if verr := validatePayload(kind, raw); verr != nil {
		return nil, verr
	}
	return raw, nil
}
