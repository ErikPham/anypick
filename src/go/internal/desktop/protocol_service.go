package desktop

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/buildinfo"
	"github.com/outof0/anypick/go/internal/config"
	"github.com/outof0/anypick/go/internal/db"
)

// ServiceProtocolVersion is the app-owned stdio contract. Version 2 adds a
// mandatory, mutation-gating handshake while deliberately retaining the
// already-reviewed v1 command and snapshot DTOs (ADR 0020 §6).
const ServiceProtocolVersion = buildinfo.ProtocolVersion

// MaxServiceFrameBytes bounds every line accepted by the app service. The
// snapshot is intentionally allowed to be larger than an individual command,
// but remains finite so a corrupt peer cannot grow memory without limit.
const MaxServiceFrameBytes = 4 * 1024 * 1024

var serviceCapabilities = []string{
	"cancel-v1",
	"command-dto-v1",
	"explicit-refresh",
	"host-effects-v1",
	"post-mutation-events",
	"snapshot-dto-v1",
	"usage-events-v1",
}

var requiredHostCapabilities = []string{"command-dto-v1", "snapshot-dto-v1"}

// EngineHello is the first frame emitted by desktop-service. InstanceID is a
// diagnostic correlation id, not an authentication token and grants no access.
type EngineHello struct {
	ProtocolVersion int      `json:"protocolVersion"`
	MessageVersion  int      `json:"messageVersion"`
	EngineVersion   string   `json:"engineVersion"`
	SchemaVersion   int      `json:"schemaVersion"`
	Capabilities    []string `json:"capabilities"`
	InstanceID      string   `json:"instanceId"`
	MaxFrameBytes   int      `json:"maxFrameBytes"`
}

// HostHello is the native shell's acknowledgement. No command is decoded or
// executed until this exact frame is accepted.
type HostHello struct {
	ProtocolVersion int             `json:"protocolVersion"`
	AppVersion      string          `json:"appVersion"`
	Platform        string          `json:"platform"`
	Capabilities    []string        `json:"capabilities"`
	HostState       HostStateReport `json:"hostState"`
}

type HostStateReport struct {
	LaunchAtLogin bool `json:"launchAtLogin"`
}

type HandshakeReady struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities"`
}

type HandshakeRejected struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

type ProtocolError struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Code            string `json:"code"`
	Message         string `json:"message"`
}

type SnapshotEvent struct {
	ProtocolVersion int       `json:"protocolVersion"`
	Revision        int       `json:"revision"`
	Reason          string    `json:"reason"`
	Snapshot        *Snapshot `json:"snapshot"`
}

type UsageEvent struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Revision        int                `json:"revision"`
	Usage           []UsageSnapshotRow `json:"usage"`
}

type HostEffectFrame struct {
	ProtocolVersion int                      `json:"protocolVersion"`
	RequestID       string                   `json:"requestId"`
	Effect          config.HostEffectRequest `json:"effect"`
}

type HostEffectResult struct {
	ProtocolVersion int             `json:"protocolVersion"`
	RequestID       string          `json:"requestId"`
	Status          string          `json:"status"`
	Message         string          `json:"message,omitempty"`
	HostState       HostStateReport `json:"hostState"`
}

func newEngineHello() EngineHello {
	caps := append([]string(nil), serviceCapabilities...)
	sort.Strings(caps)
	return EngineHello{
		ProtocolVersion: ServiceProtocolVersion,
		MessageVersion:  ProtocolVersion,
		EngineVersion:   buildinfo.Version,
		SchemaVersion:   db.CurrentSchemaVersion(),
		Capabilities:    caps,
		InstanceID:      newDiagnosticInstanceID(),
		MaxFrameBytes:   MaxServiceFrameBytes,
	}
}

func newDiagnosticInstanceID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	// crypto/rand failure is not a reason to expose or derive a machine id.
	return "unavailable"
}

func frameService(prefix string, value any) string {
	raw, _ := json.Marshal(value)
	return frame(prefix, json.RawMessage(raw))
}

func FrameEngineHello(hello EngineHello) string { return frameService("hello", hello) }

func FrameHostHello(hello HostHello) string { return frameService("hello", hello) }

func FrameHandshakeReady(ready HandshakeReady) string {
	return frameService("ready", ready)
}

func FrameHandshakeRejected(rejected HandshakeRejected) string {
	return frameService("handshake-rejected", rejected)
}

func FrameProtocolError(protocolErr ProtocolError) string {
	return frameService("protocol-error", protocolErr)
}

func FrameSnapshotEvent(event SnapshotEvent) string {
	return frameService("snapshot-event", event)
}

func FrameUsageEvent(event UsageEvent) string { return frameService("usage-event", event) }

func FrameHostEffect(effect HostEffectFrame) string {
	return frameService("host-effect", effect)
}

func FrameHostEffectResult(result HostEffectResult) string {
	return frameService("host-effect-result", result)
}

func decodeHostHello(line string) (HostHello, error) {
	const prefix = "hello\t"
	if len(line) > MaxServiceFrameBytes || !strings.HasPrefix(line, prefix) {
		return HostHello{}, fmt.Errorf("first host frame must be hello")
	}
	var hello HostHello
	if err := decodeB64Strict(strings.TrimPrefix(line, prefix), &hello); err != nil {
		return HostHello{}, err
	}
	if hello.ProtocolVersion != ServiceProtocolVersion {
		return HostHello{}, fmt.Errorf("protocol version %d is not supported", hello.ProtocolVersion)
	}
	if !safeHandshakeString(hello.AppVersion, 128) {
		return HostHello{}, fmt.Errorf("invalid app version")
	}
	switch hello.Platform {
	case "macos", "windows", "linux", "test":
	default:
		return HostHello{}, fmt.Errorf("unsupported host platform %q", hello.Platform)
	}
	if len(hello.Capabilities) == 0 || len(hello.Capabilities) > 64 {
		return HostHello{}, fmt.Errorf("invalid host capabilities")
	}
	seen := make(map[string]bool, len(hello.Capabilities))
	for _, capability := range hello.Capabilities {
		if !safeHandshakeString(capability, 64) || seen[capability] {
			return HostHello{}, fmt.Errorf("invalid host capability")
		}
		seen[capability] = true
	}
	for _, required := range requiredHostCapabilities {
		if !seen[required] {
			return HostHello{}, fmt.Errorf("host is missing required capability %q", required)
		}
	}
	if !compatibleBuildVersions(hello.AppVersion, buildinfo.Version) {
		return HostHello{}, fmt.Errorf("app version %s does not match engine version %s",
			hello.AppVersion, buildinfo.Version)
	}
	return hello, nil
}

func decodeHostEffectResult(line string) (HostEffectResult, error) {
	const prefix = "host-effect-result\t"
	if len(line) > MaxServiceFrameBytes || !strings.HasPrefix(line, prefix) {
		return HostEffectResult{}, fmt.Errorf("expected host-effect-result frame")
	}
	var result HostEffectResult
	if err := decodeB64Strict(strings.TrimPrefix(line, prefix), &result); err != nil {
		return HostEffectResult{}, err
	}
	if result.ProtocolVersion != ServiceProtocolVersion || !safeStr(result.RequestID, 128) {
		return HostEffectResult{}, fmt.Errorf("invalid host-effect result identity")
	}
	if result.Status != "success" && result.Status != "error" {
		return HostEffectResult{}, fmt.Errorf("invalid host-effect result status")
	}
	if !safeOptional(result.Message, 512) {
		return HostEffectResult{}, fmt.Errorf("invalid host-effect result message")
	}
	return result, nil
}

func decodeB64Strict(encoded string, into any) error {
	if encoded == "" || len(encoded) > MaxServiceFrameBytes {
		return fmt.Errorf("bad handshake payload length")
	}
	raw, err := decodeCanonicalBase64(encoded)
	if err != nil {
		return err
	}
	if len(raw) > MaxServiceFrameBytes {
		return fmt.Errorf("decoded handshake payload too large")
	}
	return decodeStrict(raw, into)
}

func decodeCanonicalBase64(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("bad base64: %w", err)
	}
	if base64.StdEncoding.EncodeToString(raw) != encoded {
		return nil, fmt.Errorf("non-canonical base64")
	}
	return raw, nil
}

func safeHandshakeString(value string, max int) bool {
	return value != "" && len(value) <= max && safeOptional(value, max)
}

func compatibleBuildVersions(appVersion, engineVersion string) bool {
	if appVersion == "(devel)" || engineVersion == "(devel)" {
		return true
	}
	return appVersion == engineVersion
}

func negotiatedCapabilities(host []string) []string {
	hostSet := make(map[string]bool, len(host))
	for _, capability := range host {
		hostSet[capability] = true
	}
	result := make([]string, 0, len(serviceCapabilities))
	for _, capability := range serviceCapabilities {
		if hostSet[capability] {
			result = append(result, capability)
		}
	}
	sort.Strings(result)
	return result
}

// HostPlatform reports the compile target in handshake-shaped vocabulary.
// It is useful to native harnesses without exposing filesystem or credentials.
func HostPlatform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}
