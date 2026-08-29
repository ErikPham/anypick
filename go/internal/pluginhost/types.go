// Package pluginhost supervises the private Node process used to execute
// trusted ESM plugins. It is transport-only: plugin registry persistence and
// native registry sealing belong to higher layers.
package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/outof0/anypick/go/internal/provider"
)

const (
	// ProtocolVersion is the newline-JSON transport generation.
	ProtocolVersion = 1
	// PluginAPIVersion is the AnyPick JavaScript plugin context generation.
	PluginAPIVersion = 1
)

// PluginSpec identifies bytes the user previously trusted. The Node host
// independently hashes Root and refuses a mismatch before import.
type PluginSpec struct {
	Root          string `json:"root"`
	TrustedDigest string `json:"trustedDigest"`
	ExpectedName  string `json:"expectedName"`
}

// Manifest is the validated anypick.plugin.json identity returned by the host.
type Manifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	APIVersion  int    `json:"apiVersion"`
	Main        string `json:"main"`
}

// RegistrationKind identifies an executable object retained in the Node host.
type RegistrationKind string

const (
	RegistrationProvider        RegistrationKind = "provider"
	RegistrationClient          RegistrationKind = "client"
	RegistrationCatalogProvider RegistrationKind = "catalog-provider"
)

// Descriptor describes an arbitrary JavaScript registry object. Properties are
// inert JSON; method calls use Handle through Host.Invoke.
type Descriptor struct {
	Kind       RegistrationKind           `json:"kind"`
	Handle     string                     `json:"handle"`
	Properties map[string]json.RawMessage `json:"properties"`
	Methods    []string                   `json:"methods"`
}

// RemoteObject is an object returned by a plugin method that itself exposes
// executable behavior, such as SourceAdapter.transportFor,
// IsolatedClientRuntime.cleanup, or ProxyHubBackendHandle.close. Its lifetime
// is tied to the activation that returned it.
type RemoteObject struct {
	Handle     string                     `json:"handle"`
	Properties map[string]json.RawMessage `json:"properties"`
	Methods    []string                   `json:"methods"`
}

// ID reads the mandatory registry identity captured by the host.
func (d Descriptor) ID() (string, error) {
	raw, ok := d.Properties["id"]
	if !ok {
		return "", fmt.Errorf("pluginhost: descriptor %q has no id", d.Handle)
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || id == "" {
		return "", fmt.Errorf("pluginhost: descriptor %q has an invalid id", d.Handle)
	}
	return id, nil
}

// Activation is an atomic contribution returned after activate succeeds.
// CompatibleProviders are already decoded into the native Go definition;
// arbitrary objects remain RPC-backed descriptors.
type Activation struct {
	ActivationID        string                          `json:"activationId"`
	Manifest            Manifest                        `json:"manifest"`
	Descriptors         []Descriptor                    `json:"descriptors"`
	CompatibleProviders []provider.CompatibleDefinition `json:"compatibleProviders"`

	host *Host
}

// Dispose releases one activation and all of its object handles. Host.Close
// remains the normal owner and disposes every remaining activation in reverse
// activation order.
func (a *Activation) Dispose(ctx context.Context) error {
	if a == nil || a.host == nil || a.ActivationID == "" {
		return nil
	}
	var result struct {
		Disposed bool `json:"disposed"`
	}
	err := a.host.call(ctx, "dispose", map[string]string{"activationId": a.ActivationID}, &result, false)
	if err == nil {
		a.host = nil
	}
	return err
}

// Status is a diagnostic view used to prove failed activation rollback. It
// contains no plugin values or credentials.
type Status struct {
	ActivationCount int `json:"activationCount"`
	ObjectCount     int `json:"objectCount"`
}

// Health is the local process-supervision state. Error is diagnostic text
// only; it never contains plugin values or invocation arguments.
type Health struct {
	Running bool   `json:"running"`
	Closing bool   `json:"closing"`
	Error   string `json:"error,omitempty"`
}

// RemoteError is a bounded error reported by the Node host.
type RemoteError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *RemoteError) Error() string {
	if e == nil {
		return "pluginhost: remote error"
	}
	return fmt.Sprintf("pluginhost: %s: %s", e.Code, e.Message)
}

func (e *RemoteError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case "TIMEOUT":
		return context.DeadlineExceeded
	case "CANCELLED":
		return context.Canceled
	default:
		return nil
	}
}
