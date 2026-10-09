// Package transport classifies capability pairs through account adapters.
//
// INVARIANT (AGENTS.md #1): capability pairs come from these adapters. The
// client registry lists what exists, never what is allowed; adapter_matrix_test.go
// carries the full matrix. gemini and kiro clients return Unsupported for
// anything but their own. A workbuddy *account* serves claude, codex,
// opencode, zcode, dsh, commandcode, pi and omp through the proxy, while the
// workbuddy *client* uses its own login directly. cline is a provider login
// with no registered client adapter; its own account adapter classifies the
// cline client ManagedBuiltinProxy, and every other adapter returns
// Unsupported for it.
package transport

import (
	"github.com/outof0/anypick/go/internal/anyerr"
)

// Capability is the transport classification for one client.
type Capability string

const (
	Direct               Capability = "direct"
	ManagedBuiltinProxy  Capability = "managed_builtin_proxy"
	ManagedExternalProxy Capability = "managed_external_proxy"
	ExternalManualProxy  Capability = "external_manual_proxy"
	Unsupported          Capability = "unsupported"
)

// Protocol is the client protocol ('openai' | 'anthropic').
type Protocol string

const (
	OpenAI    Protocol = "openai"
	Anthropic Protocol = "anthropic"
)

// Policy is the transport policy ('auto' | 'direct' | 'proxy').
type Policy string

const (
	PolicyAuto   Policy = "auto"
	PolicyDirect Policy = "direct"
	PolicyProxy  Policy = "proxy"
)

// Capabilities describes what a source offers.
type Capabilities struct {
	SourceKind              string // 'account' | 'gateway' | 'proxy-hub'
	Provider                string
	NativeClients           []string
	Protocols               []Protocol
	CanRefresh              bool
	SupportsModelDiscovery  bool
	RequiresNativeAuthWrite bool
}

// ProviderCanProxy is the minimal core hook the adapters need — true when the
// provider implements startProxy. Providers are modeled as data, so callers
// pass the boolean their provider registry reports.
type ProviderInfo struct {
	ID         string
	CanProxy   bool
	HasAdapter bool // provider.sourceAdapter implemented
}

// Adapter pairs a source with its classification functions.
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
// the cross-provider clients (claude/codex/opencode/zcode/dsh/commandcode/pi/omp) and
// Unsupported for every other client, including kiro and workbuddy.
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
		case "claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// GrokAccountAdapter: built-in dual-protocol proxy for compatible clients.
// No native-auth write: the proxy authenticates per request from the account
// snapshot (grokUpstream). WriteNativeAuth is emitted only for direct
// transports — a cross-provider proxy binding like `use claude --with
// grok/x` must not rewrite grok's live login or active pointer as a side
// effect.
func GrokAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("grok", nil),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp":
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
		case "claude", "codex", "commandcode", "pi", "omp":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// WorkBuddyAccountAdapter: a WorkBuddy/CodeBuddy subscription serves the
// borrowing clients through the built-in proxy, and the WorkBuddy app itself
// uses its own login directly: binding the app to one of its accounts
// restores the desktop session file (WriteNativeAuth) and drops AnyPick's
// proxy entries from models.json, so the app runs native. Only its own
// provider pairs directly — anything else stays Unsupported.
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
		case "claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp":
			return ManagedBuiltinProxy
		default:
			return Unsupported
		}
	}
	return a
}

// ClineAccountAdapter: Cline's own subscription client is proxy-backed —
// the WorkOS login lives in the app's providers.json behind its own manager,
// so a direct pairing cannot switch the account AnyPick selected. Cross
// clients ride the same OpenAI-chat subscription gateway as WorkBuddy's.
func ClineAccountAdapter(provider, name string) Adapter {
	a := Adapter{
		Capabilities: baseAccountCapabilities("cline", func(c *Capabilities) {
			c.NativeClients = []string{"cline"}
			c.CanRefresh = false
			c.RequiresNativeAuthWrite = false
		}),
	}
	a.SourceRef = accountSourceRef(provider, name)
	a.TransportFor = func(clientID string) Capability {
		switch clientID {
		case "cline":
			return ManagedBuiltinProxy
		case "claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp":
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
		case "claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp":
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

// PoolAdapterFor builds a pool adapter: a pool serves only borrowing clients
// through a proxy. gemini/kiro are unsupported regardless of the pool.
func PoolAdapterFor(providerID string, info ProviderInfo, proxyTransport Capability, acceptedClients ...string) Adapter {
	accepted := map[string]bool{}
	for _, c := range acceptedClients {
		accepted[c] = true
	}
	if len(accepted) == 0 {
		for _, c := range []string{"claude", "codex", "opencode", "zcode", "dsh", "commandcode", "pi", "omp"} {
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

// managedProxySet is the set of managed proxy capabilities.
// ExternalManualProxy is deliberately excluded: it is not a managed proxy
// (no process to spawn/health-check), so --transport proxy must not accept it.
var managedProxySet = map[Capability]bool{
	ManagedBuiltinProxy:  true,
	ManagedExternalProxy: true,
}

// ResolveTransportPolicy resolves an adapter's provider-owned transport offer
// against explicit user intent. Auto preserves the adapter default. Explicit
// policy never falls back to another path: a direct request either resolves
// to direct or fails.
func ResolveTransportPolicy(a Adapter, clientID string, policy Policy) (Capability, error) {
	automatic := a.TransportFor(clientID)
	if policy == "" || policy == PolicyAuto {
		return automatic, nil
	}
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
