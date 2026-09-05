// Package transport ports src/sources/account-adapters.ts and
// src/core/capabilities.ts.
//
// INVARIANT (AGENTS.md #1): capability pairs come from these adapters.
// claude, codex, opencode, zcode, and dsh may accept a source from a different
// provider; gemini and kiro return Unsupported for anything but their own. The
// client registry lists what exists, never what is allowed.
package transport

import (
	"github.com/outof0/anypick/go/internal/anyerr"
)

// Capability mirrors TransportCapability.
type Capability string

const (
	Direct               Capability = "direct"
	ManagedBuiltinProxy  Capability = "managed_builtin_proxy"
	ManagedExternalProxy Capability = "managed_external_proxy"
	ExternalManualProxy  Capability = "external_manual_proxy"
	Unsupported          Capability = "unsupported"
)

// Protocol mirrors Protocol ('openai' | 'anthropic').
type Protocol string

const (
	OpenAI    Protocol = "openai"
	Anthropic Protocol = "anthropic"
)

// Policy mirrors TransportPolicy ('auto' | 'direct' | 'proxy').
type Policy string

const (
	PolicyAuto   Policy = "auto"
	PolicyDirect Policy = "direct"
	PolicyProxy  Policy = "proxy"
)

// Capabilities mirrors SourceCapabilities.
type Capabilities struct {
	SourceKind              string // 'account' | 'gateway' | 'proxy-hub'
	Provider                string
	NativeClients           []string
	Protocols               []Protocol
	CanRefresh              bool
	SupportsModelDiscovery  bool
	RequiresNativeAuthWrite bool
}

// ProviderCanProxy is the minimal core hook the adapters need. In TS this is
// src/core/capabilities.ts providerCanProxy(provider) — true when the provider
// implements startProxy. The Go side models providers as data, so callers pass
// the boolean their provider registry reports.
type ProviderInfo struct {
	ID         string
	CanProxy   bool
	HasAdapter bool // provider.sourceAdapter implemented
}

// Adapter mirrors SourceAdapter: a value plus its classification functions.
type Adapter struct {
	SourceRef struct {
		Kind     string
		Provider string
		Name     string
	}
	Capabilities Capabilities
	TransportFor func(clientID string) Capability
	// TransportForPolicy is optional; nil falls back to TransportFor.
	TransportForPolicy func(clientID string, policy Policy) Capability
}

func baseAccountCapabilities(provider string, mutate func(*Capabilities)) Capabilities {
	caps := Capabilities{
		SourceKind:              "account",
		Provider:                provider,
		NativeClients:           []string{},
		Protocols:               []Protocol{OpenAI, Anthropic},
		CanRefresh:              false,
		SupportsModelDiscovery:  false,
		RequiresNativeAuthWrite: false,
	}
	if mutate != nil {
		mutate(&caps)
	}
	return caps
}

func accountSourceRef(provider, name string) (ref struct {
	Kind     string
	Provider string
	Name     string
}) {
	ref.Kind = "account"
	ref.Provider = provider
	ref.Name = name
	return
}

// CodexAccountAdapter: direct for codex only; unsupported for others without
// its proxy. Codex native auth is not a gateway for Claude/Kiro without it.
func CodexAccountAdapter(provider, name string, proxyEnabled bool) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("codex", func(c *Capabilities) {
			c.NativeClients = []string{"codex"}
			c.Protocols = []Protocol{OpenAI}
			c.CanRefresh = true
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		if clientID == "codex" {
			return Direct
		}
		return Unsupported
	}
	a.TransportForPolicy = func(clientID string, policy Policy) Capability {
		if clientID != "codex" {
			return Unsupported
		}
		if policy == PolicyDirect {
			return Direct
		}
		// Policy selects an available path; it cannot enable a service itself.
		if proxyEnabled {
			return ManagedBuiltinProxy
		}
		return Unsupported
	}
	return a
}

// ClaudeAccountAdapter: direct only for Claude Code.
func ClaudeAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("claude", func(c *Capabilities) {
			c.NativeClients = []string{"claude"}
			c.Protocols = []Protocol{Anthropic}
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		if clientID == "claude" {
			return Direct
		}
		return Unsupported
	}
	return a
}

// GeminiAccountAdapter: direct for gemini ONLY (invariant); builtin proxy for
// claude/codex/opencode/zcode.
func GeminiAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("gemini", func(c *Capabilities) {
			c.NativeClients = []string{"gemini"}
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "gemini":
			return Direct
		case "claude", "codex", "opencode", "zcode", "dsh":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// GrokAccountAdapter: built-in dual-protocol proxy for compatible clients.
func GrokAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("grok", nil),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "claude", "codex", "opencode", "zcode", "dsh":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// OpenCodeAccountAdapter: direct for opencode; Zen/Go keys can also back a
// proxy for claude/codex.
func OpenCodeAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("opencode", func(c *Capabilities) {
			c.NativeClients = []string{"opencode"}
			c.CanRefresh = true
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "opencode":
			return Direct
		case "claude", "codex":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// WorkBuddyAccountAdapter: direct for workbuddy; its OpenAI-compatible accounts
// can also back claude/codex/opencode/zcode through the managed builtin proxy
// (same shape as OpenCodeAccountAdapter).
func WorkBuddyAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("workbuddy", func(c *Capabilities) {
			c.NativeClients = []string{"workbuddy"}
			c.CanRefresh = false
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "workbuddy":
			return Direct
		case "claude", "codex", "opencode", "zcode", "dsh":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// KiroAccountAdapter: external kirolink proxy. Classification shares the
// binary resolver with the launcher so gate and spawn cannot disagree.
// Missing binary → ExternalManualProxy (activation exits 7 before mutation).
// findExecutable returns the resolved kirolink command or "" when absent.
func KiroAccountAdapter(provider, name string, findExecutable func() string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("kiro", func(c *Capabilities) {
			c.NativeClients = []string{"kiro"}
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "kiro":
			return Direct
		case "claude", "codex", "opencode", "zcode", "dsh":
			if findExecutable != nil && findExecutable() != "" {
				return ManagedExternalProxy
			}
			return ExternalManualProxy
		default:
			return Unsupported
		}
	}
	return a
}

// PoolAdapterFor mirrors poolAdapterFor: a pool serves only borrowing clients
// through a proxy. gemini/kiro are unsupported regardless of the pool.
func PoolAdapterFor(providerID string, info ProviderInfo, proxyTransport Capability, acceptedClients ...string) Adapter {
	accepted := map[string]bool{}
	for _, c := range acceptedClients {
		accepted[c] = true
	}
	if len(accepted) == 0 {
		for _, c := range []string{"claude", "codex", "opencode", "zcode", "dsh"} {
			accepted[c] = true
		}
	}
	if proxyTransport != ManagedBuiltinProxy && proxyTransport != ManagedExternalProxy {
		proxyTransport = ManagedBuiltinProxy
	}
	a := Adapter{
		Capabilities: baseAccountCapabilities(providerID, func(c *Capabilities) {
			c.CanRefresh = false
			c.RequiresNativeAuthWrite = false
		}),
	}
	a.SourceRef.Kind = "account-pool"
	a.SourceRef.Provider = providerID
	a.TransportFor = func(clientID string) Capability {
		if !info.CanProxy {
			return Unsupported
		}
		if accepted[clientID] {
			return proxyTransport
		}
		return Unsupported
	}
	return a
}

// GenericAccountAdapter is the conservative fallback for third-party providers
// without the extension contract.
func GenericAccountAdapter(provider, name string, info ProviderInfo) Adapter {
	native := []string{}
	if provider == info.ID {
		native = []string{info.ID}
	}
	a := Adapter{
		Capabilities: baseAccountCapabilities(info.ID, func(c *Capabilities) {
			c.NativeClients = native
			c.RequiresNativeAuthWrite = true
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		if clientID == info.ID {
			return Direct
		}
		// Cross-provider transport stays deliberately narrow: the client
		// registry says what exists; this adapter remains the authority for
		// what is safe to pair (ADR 0011).
		if info.CanProxy {
			switch clientID {
			case "claude", "codex", "opencode", "zcode", "dsh":
				return ManagedBuiltinProxy
			}
		}
		return Unsupported
	}
	return a
}

// AccountAdapterFor mirrors accountAdapterFor: providers own their transport
// policy; core has only the generic fallback. adapterFor returns the
// provider-specific adapter when the provider implements sourceAdapter.
func AccountAdapterFor(info ProviderInfo, provider, name string, proxyEnabled bool, adapterFor func() *Adapter) Adapter {
	if info.HasAdapter && adapterFor != nil {
		if a := adapterFor(); a != nil {
			return *a
		}
	}
	return GenericAccountAdapter(provider, name, info)
}

// managedProxySet mirrors MANAGED_PROXY in src/core/transport-resolver.ts.
// ExternalManualProxy is deliberately excluded: it is not a managed proxy
// (no process to spawn/health-check), so --transport proxy must not accept it.
var managedProxySet = map[Capability]bool{
	ManagedBuiltinProxy:  true,
	ManagedExternalProxy: true,
}

// ResolveTransportPolicy ports resolveTransportPolicy
// (src/core/transport-resolver.ts): resolve an adapter's provider-owned
// transport offer against explicit user intent. Auto preserves the adapter
// default. Explicit policy never falls back to another path: a direct request
// either resolves to direct or fails.
func ResolveTransportPolicy(a Adapter, clientID string, policy Policy) (Capability, error) {
	automatic := a.TransportFor(clientID)
	if policy == "" || policy == PolicyAuto {
		return automatic, nil
	}
	// The policy-aware override runs first; adapters without one fall back to
	// their automatic classification.
	selected := automatic
	if a.TransportForPolicy != nil {
		selected = a.TransportForPolicy(clientID, policy)
	}
	if policy == PolicyDirect && selected == Direct {
		return selected, nil
	}
	if policy == PolicyProxy && managedProxySet[selected] {
		return selected, nil
	}

	wanted := "local proxy"
	suggestion := "Enable a supported local proxy for this source, or use --transport auto/direct."
	if policy == PolicyDirect {
		wanted = "direct connection"
		suggestion = "Use --transport auto or proxy, or choose a compatible direct endpoint."
	}
	return Unsupported, anyerr.Newf(
		"Source "+a.SourceRef.Kind+" does not support "+wanted+" for client "+clientID+".",
		"UNSUPPORTED_TRANSPORT",
		anyerr.WithExitCode(anyerr.CapabilityConflict),
		anyerr.WithSuggestions(suggestion),
	)
}
