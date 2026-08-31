// Package refs ports src/core/refs.ts: deterministic resource reference
// parsing (spec §15) and the two distinct scope functions.
//
// TRAP (AGENTS.md): mutationScopeForRef is NOT serializeRef. The former is the
// lock scope (accounts collapse onto their provider, ADR 0011); the latter is
// the precise identity used for journal affectedResources. Never swap them.
package refs

import (
	"fmt"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// Kind enumerates ResourceKind from src/types/bindings.ts.
type Kind string

const (
	KindAccount     Kind = "account"
	KindGateway     Kind = "gateway"
	KindProxyHub    Kind = "proxy-hub"
	KindPreset      Kind = "preset"
	KindAccountPool Kind = "account-pool"
)

// Ref is the flattened form of the TS discriminated union. Provider is set for
// account/account-pool; Name for account/gateway/proxy-hub/preset.
type Ref struct {
	Kind     Kind
	Provider string
	Name     string
}

// DefaultAccountProviders mirrors DEFAULT_ACCOUNT_PROVIDERS: known
// file-snapshot account providers.
func DefaultAccountProviders() map[string]bool {
	return map[string]bool{
		"codex": true, "gemini": true, "grok": true, "kiro": true, "opencode": true,
	}
}

// ParseContext carries optional known-provider overrides.
type ParseContext struct {
	AccountProviders map[string]bool
}

// ProviderScope covers a provider's live credential file AND all of its saved
// snapshots — deliberately coarser than one lock per account (ADR 0009/0011).
func ProviderScope(providerID string) string { return "provider/" + providerID }

// SerializeRef returns the precise resource identity (journal affectedResources).
// Unknown kinds return a stable invalid sentinel instead of panicking so a
// corrupt row cannot crash the process (P1 — §6).
func SerializeRef(ref Ref) string {
	switch ref.Kind {
	case KindAccount:
		return fmt.Sprintf("account/%s/%s", ref.Provider, ref.Name)
	case KindGateway:
		return "gateway/" + ref.Name
	case KindProxyHub:
		return "hub/" + ref.Name
	case KindPreset:
		return "preset/" + ref.Name
	case KindAccountPool:
		return "pool/" + ref.Provider
	default:
		return fmt.Sprintf("invalid:%s", string(ref.Kind))
	}
}

// MutationScopeForRef returns the lock scope an activation must hold. Account
// and pool sources both reach the provider's single live auth file, so they
// collapse onto ProviderScope; everything else locks its exact identity.
func MutationScopeForRef(ref Ref) string {
	switch ref.Kind {
	case KindAccount, KindAccountPool:
		return ProviderScope(ref.Provider)
	case KindGateway, KindProxyHub, KindPreset:
		return SerializeRef(ref)
	default:
		return fmt.Sprintf("invalid:%s", string(ref.Kind))
	}
}

// DisplayRef renders a user-facing label.
func DisplayRef(ref Ref) string {
	switch ref.Kind {
	case KindAccount:
		return fmt.Sprintf("%s/%s", ref.Provider, ref.Name)
	case KindGateway:
		return ref.Name
	case KindProxyHub:
		return "hub:" + ref.Name
	case KindPreset:
		return "@" + ref.Name
	case KindAccountPool:
		return "pool:" + ref.Provider
	default:
		return fmt.Sprintf("invalid:%s", string(ref.Kind))
	}
}

func AccountRef(provider, name string) Ref {
	return Ref{Kind: KindAccount, Provider: provider, Name: name}
}
func GatewayRef(name string) Ref  { return Ref{Kind: KindGateway, Name: name} }
func ProxyHubRef(name string) Ref { return Ref{Kind: KindProxyHub, Name: name} }
func PresetRef(name string) Ref   { return Ref{Kind: KindPreset, Name: name} }
func AccountPoolRef(provider string) Ref {
	return Ref{Kind: KindAccountPool, Provider: provider}
}

func invalidRef(msg string, suggestions ...string) error {
	return anyerr.Newf(msg, "INVALID_REFERENCE",
		anyerr.WithExitCode(anyerr.InvalidUsage),
		anyerr.WithSuggestions(suggestions...),
	)
}

// ParseRef ports parseRef: grammar-only parsing, no existence checks.
//
// Grammar:
//
//	provider/account         → account
//	account/provider/account → account
//	gateway-name             → gateway
//	gateway/name             → gateway
//	@preset / preset/name    → preset
//	hub:name | hub/name      → proxy-hub
//	pool:name | pool/name    → account-pool
func ParseRef(input string, ctx *ParseContext) (Ref, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return Ref{}, invalidRef(
			"Empty resource reference.",
			"Accounts use provider/name:  grok/work",
			"Gateways use their saved name:  openrouter-work",
			"Presets use @name:  @work-grok",
		)
	}

	providers := DefaultAccountProviders()
	if ctx != nil && ctx.AccountProviders != nil {
		providers = ctx.AccountProviders
	}

	if strings.HasPrefix(raw, "@") {
		name := strings.TrimSpace(raw[1:])
		if name == "" {
			return Ref{}, invalidRef("Preset name is required after @.")
		}
		return PresetRef(name), nil
	}

	if strings.HasPrefix(raw, "preset/") {
		name := strings.TrimSpace(raw[len("preset/"):])
		if name == "" {
			return Ref{}, invalidRef("Preset name is required after preset/.")
		}
		return PresetRef(name), nil
	}

	if rest, ok := trimPrefixAny(raw, "hub:", "hub/"); ok {
		name := strings.TrimSpace(rest)
		if name == "" || strings.ContainsAny(name, "/:") {
			return Ref{}, invalidRef(fmt.Sprintf("Invalid Proxy Hub reference: %s", raw))
		}
		return ProxyHubRef(name), nil
	}

	if rest, ok := trimPrefixAny(raw, "pool:", "pool/"); ok {
		provider := strings.TrimSpace(rest)
		if provider == "" || strings.ContainsAny(provider, "/:") {
			return Ref{}, invalidRef(
				fmt.Sprintf("Invalid pool reference: %s. Expected pool:provider (e.g. pool:gemini)", raw),
			)
		}
		if !providers[provider] {
			return Ref{}, invalidRef(
				fmt.Sprintf("Unknown account provider %q in pool reference: %s", provider, raw),
				"Known: "+sortedKeys(providers),
			)
		}
		return AccountPoolRef(provider), nil
	}

	if strings.HasPrefix(raw, "gateway/") {
		name := strings.TrimSpace(raw[len("gateway/"):])
		if name == "" || strings.Contains(name, "/") {
			return Ref{}, invalidRef(fmt.Sprintf("Invalid gateway reference: %s", raw))
		}
		return GatewayRef(name), nil
	}

	if strings.HasPrefix(raw, "account/") {
		rest := raw[len("account/"):]
		slash := strings.Index(rest, "/")
		if slash <= 0 || slash == len(rest)-1 {
			return Ref{}, invalidRef(
				fmt.Sprintf("Invalid account reference: %s. Expected account/provider/name", raw),
			)
		}
		return AccountRef(rest[:slash], rest[slash+1:]), nil
	}

	if slash := strings.Index(raw, "/"); slash > 0 {
		provider, name := raw[:slash], raw[slash+1:]
		if providers[provider] {
			if name == "" || strings.Contains(name, "/") {
				return Ref{}, invalidRef(
					fmt.Sprintf("Invalid account reference: %s. Expected provider/account", raw),
				)
			}
			return AccountRef(provider, name), nil
		}
		// Unknown provider with slash: invalid account form, never a gateway.
		return Ref{}, invalidRef(
			fmt.Sprintf("Unknown account provider %q in reference: %s", provider, raw),
			"Known account providers: "+sortedKeys(providers),
			"Gateways use a plain name without /",
			"Presets use @name",
		)
	}

	if strings.Contains(raw, "/") || strings.HasPrefix(raw, "@") {
		return Ref{}, invalidRef(fmt.Sprintf("Invalid gateway name: %s", raw))
	}
	return GatewayRef(raw), nil
}

// ParseNativeAccountShorthand accepts `codex/personal` only when the provider
// is also a supported client (native same-provider shorthand).
func ParseNativeAccountShorthand(input string, accountProviders, clientIDs map[string]bool) (Ref, bool) {
	raw := strings.TrimSpace(input)
	if raw == "" ||
		strings.HasPrefix(raw, "@") ||
		strings.HasPrefix(raw, "account/") ||
		strings.HasPrefix(raw, "gateway/") ||
		strings.HasPrefix(raw, "preset/") {
		return Ref{}, false
	}
	slash := strings.Index(raw, "/")
	if slash <= 0 {
		return Ref{}, false
	}
	provider, name := raw[:slash], raw[slash+1:]
	if name == "" || strings.Contains(name, "/") {
		return Ref{}, false
	}
	if !accountProviders[provider] || !clientIDs[provider] {
		return Ref{}, false
	}
	return AccountRef(provider, name), true
}

func trimPrefixAny(s string, prefixes ...string) (string, bool) {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return s[len(p):], true
		}
	}
	return "", false
}

func sortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
