// Tray-local settings outside the SQLite global config document.
//
// ADR 0020 §7: launch-at-login is a **typed host effect**. The Go engine never
// mutates OS integration state (LaunchAgents plists, SMAppService) itself — it
// emits a HostEffectRequest that the Swift/Tauri host executes and reports
// back through the host-state channel. The engine-side policy below is all
// that lives here.
package config

// LoginItemScope is the mutation scope for login-item changes
// (ADR 0009). The native host performs the platform write; the engine still
// serializes the decision under this scope so two toggles cannot interleave.
const LoginItemScope = "tray/login-item"

// HostEffectKind enumerates platform operations the host executes on behalf of
// the engine. Keep this closed: every entry needs a native implementation and
// a protocol test.
type HostEffectKind string

const (
	// HostEffectLaunchAtLogin registers (enabled=true) or unregisters the app
	// with the platform login-items service (SMAppService on macOS).
	HostEffectLaunchAtLogin HostEffectKind = "launch-at-login"
)

// HostEffectRequest is the typed request carried to the native host.
type HostEffectRequest struct {
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

// NewLaunchAtLoginEffect builds the request for setting-launch-at-login.
func NewLaunchAtLoginEffect(enabled bool) HostEffectRequest {
	return HostEffectRequest{Kind: string(HostEffectLaunchAtLogin), Enabled: enabled}
}

// ValidLaunchSurface reports whether s is a configurable surface.
func ValidLaunchSurface(s string) bool { return s == "tui" || s == "tray" }
