// Package client ports src/clients/*: adapters that point AI client apps at
// accounts/gateways by writing their native configuration.
package client

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/outof0/anypick/go/internal/pubgate"
	"github.com/outof0/anypick/go/internal/transport"
)

// Capabilities mirrors ClientCapabilities.
type Capabilities struct {
	ID                         string
	AcceptedProtocols          []transport.Protocol
	SupportsEnvironmentOverlay bool
	SupportsIsolatedHome       bool
	SupportsPersistentConfig   bool
	ProtocolPreference         transport.Protocol // empty = none declared
}

// ApplyContext carries everything an adapter needs to write config. It is the
// flattened form of TS ApplyContext.profile + flags (the Go profile store is
// resolved by callers before Apply).
type ApplyContext struct {
	Endpoint      string
	APIKey        string
	DefaultModel  string
	ModelRoles    map[string]string // role id → model id
	ExtraEnv      map[string]string // overlay env filtered per adapter
	DryRun        bool
	Verbose       bool
	AnyPickRoot   string
	Home          string // live client home (~)
	IsolatedHome  string // non-empty for ephemeral runtimes
	ProxyEndpoint string
	// Resolved source/transport fields let an optional ApplyPersistent adapter
	// receive the same plan identity as the TS client contract. Built-ins ignore
	// them and continue through Apply.
	SourceKind          string
	SourceProvider      string
	SourceName          string
	TransportCapability transport.Capability
	Mode                string
}

// ApplyResult mirrors {managedPaths, managedEnvKeys}.
type ApplyResult struct {
	ManagedPaths   []string
	ManagedEnvKeys []string
}

// PersistentAdapter is the optional applyPersistent convenience from the TS
// ClientAdapter contract. Runtime code prefers it only for durable/project
// writes; isolated runs always use their dedicated capability.
type PersistentAdapter interface {
	ApplyPersistent(ctx context.Context, input *ApplyContext) (*ApplyResult, error)
}

// IsolatablePath mirrors the explicit live-path allowlist a client may copy
// into an isolated runtime.
type IsolatablePath struct {
	SourcePath      string `json:"sourcePath"`
	DestinationPath string `json:"destinationPath"`
	Kind            string `json:"kind"`
	Required        bool   `json:"required"`
}

// EphemeralContext is the JSON-safe subset of ResolvedClientPlan needed by an
// arbitrary plugin client to construct its isolated home or env overlay.
type EphemeralContext struct {
	ClientID            string
	Endpoint            string
	APIKey              string
	DefaultModel        string
	ModelRoles          map[string]string
	AnyPickRoot         string
	Home                string
	SourceKind          string
	SourceProvider      string
	SourceName          string
	TransportCapability transport.Capability
}

// IsolatedRuntime is an owner-only runtime handle. Cleanup is guarded here so
// a plugin's retained cleanup hook is invoked at most once even when both a
// failed activation and the caller attempt rollback.
type IsolatedRuntime struct {
	Directory   string
	Home        string
	Environment map[string]string
	Args        []string

	cleanupOnce sync.Once
	cleanup     func() error
	cleanupErr  error
}

func NewIsolatedRuntime(
	directory, home string,
	environment map[string]string,
	args []string,
	cleanup func() error,
) *IsolatedRuntime {
	return &IsolatedRuntime{
		Directory: directory, Home: home, Environment: environment,
		Args: args, cleanup: cleanup,
	}
}

func (r *IsolatedRuntime) Cleanup() error {
	if r == nil {
		return nil
	}
	r.cleanupOnce.Do(func() {
		if r.cleanup != nil {
			r.cleanupErr = r.cleanup()
		}
	})
	return r.cleanupErr
}

// EphemeralAdapter owns an arbitrary client's isolated-home or environment
// overlay construction. Its implementation must not mutate live client state.
type EphemeralAdapter interface {
	CreateEphemeralRuntime(ctx context.Context, input *EphemeralContext) (*IsolatedRuntime, error)
}

// InspectResult mirrors ClientInspectResult (subset).
type InspectResult struct {
	Present bool
	Summary string
	Issues  []string
	// ConfigPaths mirrors the TS inspect shape (configPaths) so plugin
	// clients can report which files they manage.
	ConfigPaths []string
}

// Adapter mirrors ClientAdapter.
type Adapter interface {
	ID() string
	Name() string
	Capabilities() *Capabilities
	Apply(ctx *ApplyContext) (*ApplyResult, error)
	// Reset removes AnyPick-managed configuration, restoring backups where kept.
	Reset() error
	Inspect() (*InspectResult, error)
}

// Registry mirrors ClientRegistry. Registration happens only during
// composition; the list of clients says what exists — never what pairings are
// allowed (that is transport.Adapter.transportFor).
type Registry struct {
	gate   *pubgate.Gate
	mu     sync.RWMutex
	items  map[string]Adapter
	order  []string
	sealed bool
}

func NewRegistry() *Registry { return &Registry{items: map[string]Adapter{}} }

// SetGate installs the shared publication lock used by Get/Has/IDs. See
// provider.Registry.SetGate.
func (r *Registry) SetGate(gate *pubgate.Gate) { r.gate = gate }

func (r *Registry) Register(a Adapter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("client registry sealed")
	}
	if _, exists := r.items[a.ID()]; !exists {
		r.order = append(r.order, a.ID())
	}
	r.items[a.ID()] = a
	return nil
}

func (r *Registry) Seal() { r.mu.Lock(); r.sealed = true; r.mu.Unlock() }

// RecoverSwap mirrors provider.Registry.RecoverSwap for the client registry:
// post-seal, CAS-guarded replacement of one plugin-owned adapter during
// plugin-host recovery (GO-PARITY-M7).
func (r *Registry) RecoverSwap(previous, next Adapter) error {
	if next == nil {
		return fmt.Errorf("recovery swap requires a next adapter")
	}
	id := next.ID()
	if previous != nil && previous.ID() != id {
		return fmt.Errorf("recovery id mismatch: %s vs %s", previous.ID(), id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.items[id]
	switch {
	case exists && previous != nil && current == previous:
	case !exists:
		r.order = append(r.order, id)
	default:
		return fmt.Errorf("client %q changed since recovery started", id)
	}
	r.items[id] = next
	return nil
}

// RecoverRemove CAS-deletes `previous` when it is still the stored value.
func (r *Registry) RecoverRemove(previous Adapter) error {
	return r.RecoverUndo(previous, nil)
}

// RecoverUndo reverses a RecoverSwap of `current`. restore=nil deletes an
// id this runtime inserted.
func (r *Registry) RecoverUndo(current, restore Adapter) error {
	if current == nil {
		return fmt.Errorf("recovery undo requires the current adapter")
	}
	id := current.ID()
	if restore != nil && restore.ID() != id {
		return fmt.Errorf("recovery id mismatch: %s vs %s", restore.ID(), id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, exists := r.items[id]
	if !exists || existing != current {
		return fmt.Errorf("client %q changed since recovery started", id)
	}
	if restore == nil {
		delete(r.items, id)
		filtered := make([]string, 0, len(r.order))
		for _, item := range r.order {
			if item != id {
				filtered = append(filtered, item)
			}
		}
		r.order = filtered
		return nil
	}
	r.items[id] = restore
	return nil
}

func (r *Registry) Get(id string) (Adapter, error) {
	r.gate.RLock()
	defer r.gate.RUnlock()
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.items[id]
	if !ok {
		return nil, fmt.Errorf("unknown client %q", id)
	}
	return a, nil
}

func (r *Registry) Has(id string) bool {
	r.gate.RLock()
	defer r.gate.RUnlock()
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.items[id]
	return ok
}

func (r *Registry) IDs() []string {
	r.gate.RLock()
	defer r.gate.RUnlock()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

// RegisterBuiltin wires claude and codex clients.
// RegisterBuiltin wires every built-in client adapter.
func RegisterBuiltin(r *Registry, home string) error {
	for _, a := range []Adapter{
		newClaude(home),
		newCodex(home),
		newGemini(home),
		newKiro(home),
		newOpenCode(home),
		newWorkBuddy(home),
		newZCode(home),
	} {
		if err := r.Register(a); err != nil {
			return err
		}
	}
	return nil
}
