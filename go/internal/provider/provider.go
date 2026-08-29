// Package provider ports src/providers/* and src/core/registry.ts for
// account-auth providers: where credentials live and how they move.
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/outof0/anypick/go/internal/browserlogin"
	"github.com/outof0/anypick/go/internal/pubgate"
	"github.com/outof0/anypick/go/internal/store"
	"github.com/outof0/anypick/go/internal/transport"
)

// LiveAuthStatus mirrors LiveAuthStatus from src/types/account.ts.
type LiveAuthStatus struct {
	Present   bool
	Identity  string
	Details   string
	AccountID string
}

// SnapshotInfo is the partial meta a Backup may contribute (identity, label…).
type SnapshotInfo struct {
	Identity        string
	Label           string
	Notes           string
	CredentialKind  string // 'native' | 'proxy-only'
	CredentialClass string // provider_api_key | coding_plan_key | external_oauth (raw)
}

// Provider is the Go surface of the TS Provider interface, restricted to what
// core needs. Optional capabilities are separate interfaces asserted at
// runtime (PreflightRestorer, LiveClearer, LiveMatcher).
type Provider interface {
	ID() string
	Name() string
	Description() string
	// CanProxy reports whether this provider implements startProxy — the input
	// to transport.ProviderInfo.CanProxy. Never inferred from the client list.
	CanProxy() bool
	// AccountAdapter returns this provider's source adapter (sourceAdapter in
	// TS). Providers own their transport policy; core never branches on id.
	AccountAdapter(accountName string, proxyEnabled bool) transport.Adapter
	DetectLive() (LiveAuthStatus, error)
	// Backup copies live auth into destDir and returns display metadata.
	Backup(destDir string) (SnapshotInfo, error)
	Restore(srcDir string) error
	DescribeSnapshot(srcDir string) (SnapshotInfo, error)
}

// ShortNamer optionally exposes a compact display name (provider.shortName).
type ShortNamer interface {
	ShortName() string
}

// ShortNameOf returns the compact name for a provider.
func ShortNameOf(p Provider) string {
	if sn, ok := p.(ShortNamer); ok && sn.ShortName() != "" {
		return sn.ShortName()
	}
	return p.Name()
}

// PreflightRestorer optionally refuses a restore that would corrupt state
// (e.g. owner app still running).
type PreflightRestorer interface {
	PreflightRestore(srcDir string) error
}

// LiveClearer optionally removes local live auth without server logout.
type LiveClearer interface {
	ClearLive() error
}

// LiveMatcher optionally compares snapshot material against live login.
// known=false means comparison would require a secret prompt (never guess).
type LiveMatcher interface {
	SnapshotMatchesLive(srcDir string) (match bool, known bool, err error)
}

// LoginMethod mirrors ProviderLoginMethod from src/types/account.ts.
// Provenance is presentation data ('official' | 'community'), not policy.
// It aliases browserlogin.Method so availability resolution stays in one
// package without duplicating the shape.
type LoginMethod = browserlogin.Method

// LoginMethodsProvider optionally exposes browser sign-in methods currently
// available on this machine. Providers own both the method list and the
// implementation so core never hardcodes OAuth endpoints or commands.
type LoginMethodsProvider interface {
	LoginMethods() []LoginMethod
}

// Loginer completes a provider-declared browser login and writes its native
// credential store. Core verifies and snapshots the live credential after this
// returns. ctx cancellation must abort the flow.
type Loginer interface {
	Login(ctx context.Context, methodID, source string) error
}

// ProxyContext mirrors ProxyContext from src/types/account.ts and crosses the
// plugin RPC boundary with exactly those TS keys (providerId, accountName,
// snapshotDir, runtimeDir, config, token). Token is the ENGINE-generated
// per-instance secret (ADR 0006): it is handed to the provider so the proxy
// can authenticate requests, but a token returned by a provider is never
// trusted or published — generation and persistence stay with the engine.
type ProxyContext struct {
	ProviderID  string
	AccountName string
	// SnapshotDir is read-only material for the proxy.
	SnapshotDir string
	// RuntimeDir holds provider-owned pid/log/state files for this proxy.
	RuntimeDir string
	Config     store.ProxyConfig
	Token      string
}

// ProxyHandle mirrors ProxyHandle from src/types/account.ts — what a
// successful start reports. InstanceID carries the PROC-01 ownership identity:
// callers must verify it through the /health instance echo, never through the
// numeric PID alone.
type ProxyHandle struct {
	Endpoint         string   `json:"endpoint"`
	Compatibility    string   `json:"compatibility,omitempty"`
	PID              int      `json:"pid,omitempty"`
	InstanceID       string   `json:"instanceId,omitempty"`
	Token            string   `json:"token,omitempty"`
	LeaseID          string   `json:"leaseId,omitempty"`
	StartedNow       bool     `json:"startedNow,omitempty"`
	LogPath          string   `json:"logPath,omitempty"`
	RealignedClients []string `json:"realignedClients,omitempty"`
}

// ProxyStatus mirrors ProxyStatus from src/types/account.ts.
type ProxyStatus struct {
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	Port          int    `json:"port,omitempty"`
	Host          string `json:"host,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	Compatibility string `json:"compatibility,omitempty"`
	PID           int    `json:"pid,omitempty"`
	LogPath       string `json:"logPath,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// ProxyStarter starts a provider-owned compatibility proxy (TS startProxy).
// Presence of this method on an adapter is the ONLY proxy-capability signal —
// it must never be inferred from client registries or protocols.
//
// Idempotency contract: a second call while a healthy process for this
// account already exists must NOT spawn a second listener. The engine's proxy
// Service enforces reuse before forwarding; implementations keep their own
// guard as defense in depth.
//
// A cancelled invocation may return an error wrapping the proxy package's
// ErrUnsettledInvocation: the host could not confirm the plugin coroutine
// finished, so it may still bind afterwards. Callers must then prove
// termination through the runtime itself — termination, never an empty
// timing window — before treating the start as cleanly compensated.
type ProxyStarter interface {
	StartProxy(ctx context.Context, pluginCtx ProxyContext) (ProxyHandle, error)
}

// ProxyStopper optionally stops the proxy (TS stopProxy). Must not fail when
// the proxy is already stopped.
type ProxyStopper interface {
	StopProxy(ctx context.Context, pluginCtx ProxyContext) error
}

// ProxyStatusReporter optionally reports running state (TS proxyStatus).
type ProxyStatusReporter interface {
	ProxyStatus(ctx context.Context, pluginCtx ProxyContext) (ProxyStatus, error)
}

// ProxyLogReader optionally returns recent log text (TS readProxyLogs).
type ProxyLogReader interface {
	ReadProxyLogs(ctx context.Context, pluginCtx ProxyContext, lines int) (string, error)
}

// Registry mirrors ProviderRegistry (src/core/registry.ts).
type Registry struct {
	gate   *pubgate.Gate
	mu     sync.RWMutex
	items  map[string]Provider
	order  []string
	sealed bool
}

func NewRegistry() *Registry { return &Registry{items: map[string]Provider{}} }

// SetGate installs the shared publication lock used by Get/Has/IDs. Recovery
// commit holds the write side; these readers take the read side. Recover*
// methods do not take the gate — the caller already holds it.
func (r *Registry) SetGate(gate *pubgate.Gate) { r.gate = gate }

func (r *Registry) Register(p Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("provider registry sealed")
	}
	if _, exists := r.items[p.ID()]; !exists {
		r.order = append(r.order, p.ID())
	}
	r.items[p.ID()] = p
	return nil
}

// ReplaceCompatible is the cutover bridge for declarative providers that were
// temporarily compiled into the Go beta before GO-PARITY-M7 existed. A trusted
// plugin definition may replace only that inert built-in shape; executable
// providers and plugin registrations can never shadow each other.
func (r *Registry) ReplaceCompatible(p *Compatible) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("provider registry sealed")
	}
	existing, exists := r.items[p.ID()]
	if !exists {
		r.order = append(r.order, p.ID())
	} else if _, ok := existing.(*Compatible); !ok {
		return fmt.Errorf("provider already registered: %s", p.ID())
	}
	r.items[p.ID()] = p
	return nil
}

func (r *Registry) Seal() { r.mu.Lock(); r.sealed = true; r.mu.Unlock() }

// RecoverSwap is the ONLY sanctioned post-seal mutation: plugin-host recovery
// (GO-PARITY-M7) replacing the exact adapter this runtime committed for a
// plugin with the re-activated one from the replacement generation. The swap
// applies only when the stored value is still `previous` — a built-in or an
// unknown id can never be clobbered, and a stale generation cannot overwrite a
// newer one. An absent id means the plugin grew a registration between
// generations; it is inserted like startup would have.
func (r *Registry) RecoverSwap(previous, next Provider) error {
	if next == nil {
		return fmt.Errorf("recovery swap requires a next provider")
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
		return fmt.Errorf("provider %q changed since recovery started", id)
	}
	r.items[id] = next
	return nil
}

// RecoverRemove CAS-deletes `previous` when it is still the stored value.
// Failed or omitted plugin re-activations use this so a terminated generation
// cannot leave a permanently unowned adapter in the registry.
func (r *Registry) RecoverRemove(previous Provider) error {
	return r.RecoverUndo(previous, nil)
}

// RecoverUndo reverses a RecoverSwap of `current`. restore=nil deletes an
// id this runtime inserted; a non-nil restore must still be the same id.
func (r *Registry) RecoverUndo(current, restore Provider) error {
	if current == nil {
		return fmt.Errorf("recovery undo requires the current provider")
	}
	id := current.ID()
	if restore != nil && restore.ID() != id {
		return fmt.Errorf("recovery id mismatch: %s vs %s", restore.ID(), id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, exists := r.items[id]
	if !exists || existing != current {
		return fmt.Errorf("provider %q changed since recovery started", id)
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

func (r *Registry) Get(id string) (Provider, error) {
	r.gate.RLock()
	defer r.gate.RUnlock()
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.items[id]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	return p, nil
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

// RegisterBuiltin wires the file-snapshot providers shipped with AnyPick.
// kiro (sqlite secret store + SSO cache) remains host-composition territory.
func RegisterBuiltin(r *Registry, home string) error {
	for _, p := range []Provider{
		newCodex(home),
		newClaude(home),
		newGemini(home),
		newGrok(home),
		newOpenCode(home),
		newKiro(home),
		newWorkBuddy(home),
	} {
		if err := r.Register(p); err != nil {
			return err
		}
	}
	return nil
}

func errSnapshotMissing(label string) error {
	return &snapshotMissingError{label: label}
}

type snapshotMissingError struct{ label string }

func (e *snapshotMissingError) Error() string {
	return "Snapshot is missing " + e.label
}
