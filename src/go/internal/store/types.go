// Package store is the SQLite-backed account store.
//
// JSON field names match the AccountMeta wire shape exactly (camelCase) so
// exported artifacts stay interchangeable. writeMeta is the atomic commit
// point for an account snapshot — invariant #4 / ADR 0004 / DATA-01.
package store

import (
	"encoding/json"
	"time"
)

// CredentialClass is the account credential classification.
type CredentialClass string

const (
	CredentialNativeSubscription CredentialClass = "native_subscription"
	CredentialProviderAPIKey     CredentialClass = "provider_api_key"
	CredentialCodingPlanKey      CredentialClass = "coding_plan_key"
	CredentialExternalOAuth      CredentialClass = "external_oauth"
	CredentialProxyOnly          CredentialClass = "proxy_only"
)

// Meta is the account metadata record. Optional fields use omitempty so
// round-tripping through SQLite keeps byte-compatible shapes.
type Meta struct {
	Name            string          `json:"name"`
	Provider        string          `json:"provider"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
	Label           string          `json:"label,omitempty"`
	Identity        string          `json:"identity,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	CredentialKind  string          `json:"credentialKind,omitempty"` // 'native' | 'proxy-only'
	CredentialClass CredentialClass `json:"credentialClass,omitempty"`
}

// ProxyConfig is one account's proxy preference; the default is disabled.
type ProxyConfig struct {
	Enabled bool                   `json:"enabled"`
	Port    int                    `json:"port,omitempty"`
	Host    string                 `json:"host,omitempty"`
	Options map[string]interface{} `json:"options,omitempty"`
}

func DefaultProxyConfig() ProxyConfig { return ProxyConfig{Enabled: false} }

type Account struct {
	Meta        Meta
	SnapshotDir string
	AccountDir  string
	Proxy       ProxyConfig
}

// ProxyRuntimeState is the persisted runtime record. Token is owner-only
// material (PROXY-01): persisted in proxy_state, never surfaced in status
// output.
type ProxyRuntimeState struct {
	AccountName   string `json:"accountName"`
	Endpoint      string `json:"endpoint"`
	Compatibility string `json:"compatibility,omitempty"`
	PID           int    `json:"pid,omitempty"`
	InstanceID    string `json:"instanceId,omitempty"`
	LogPath       string `json:"logPath,omitempty"`
	StartedAt     string `json:"startedAt"`
	Token         string `json:"token,omitempty"`
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func marshalJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Inputs are plain data structs; marshaling cannot fail in practice.
		// Return a minimal valid JSON value rather than crashing the process
		// on unexpected in-memory corruption.
		return "{}"
	}
	return string(b)
}
