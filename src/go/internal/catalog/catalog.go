// Package catalog ports src/catalog/providers.ts: declarative metadata for
// gateway vendors — what exists, what protocols they speak, and their official
// endpoints. This list says what exists; capability pairing stays in the
// source adapters (invariant #1).
package catalog

import (
	"sort"

	"github.com/outof0/anypick/go/internal/transport"
)

// APIStyle mirrors ApiStyle.
type APIStyle string

const (
	StyleOpenAI    APIStyle = "openai"
	StyleAnthropic APIStyle = "anthropic"
	StyleCustom    APIStyle = "custom"
)

// Provider mirrors CatalogProvider (metadata subset).
type Provider struct {
	ID              string
	Name            string
	Description     string
	APIStyle        APIStyle
	Protocols       []transport.Protocol
	DefaultEndpoint string
	// AnthropicEndpoint is the base Anthropic-protocol clients must use when
	// the vendor serves Messages API on a DIFFERENT root than its OpenAI one.
	// Convention: OpenAI surfaces carry a trailing /v1; Anthropic surfaces do
	// NOT (the SDK appends /v1/messages itself), e.g. OpenRouter exposes
	// chat at https://openrouter.ai/api/v1 but Messages at
	// https://openrouter.ai/api.
	AnthropicEndpoint   string
	FirstPartyClientIDs []string
}

func anthropicProtocols() []transport.Protocol {
	return []transport.Protocol{transport.Anthropic}
}

func dualProtocols() []transport.Protocol {
	return []transport.Protocol{transport.OpenAI, transport.Anthropic}
}

// Builtin returns the eight catalog entries shipped with AnyPick, mirroring
// src/catalog/providers.ts.
func Builtin() map[string]Provider {
	return map[string]Provider{
		"openrouter": {
			ID:              "openrouter",
			Name:            "OpenRouter",
			Description:     "Multi-model OpenAI-compatible gateway",
			APIStyle:        StyleOpenAI,
			Protocols:       dualProtocols(),
			DefaultEndpoint: "https://openrouter.ai/api/v1",
			// Claude-style clients must NOT receive the /v1 base: they append
			// /v1/messages themselves, which would double the segment.
			AnthropicEndpoint: "https://openrouter.ai/api",
		},
		"openai": {
			ID:                  "openai",
			Name:                "OpenAI",
			Description:         "OpenAI API",
			APIStyle:            StyleOpenAI,
			Protocols:           []transport.Protocol{transport.OpenAI},
			DefaultEndpoint:     "https://api.openai.com/v1",
			FirstPartyClientIDs: []string{"codex"},
		},
		"anthropic": {
			ID:                  "anthropic",
			Name:                "Anthropic",
			Description:         "Anthropic Claude API",
			APIStyle:            StyleAnthropic,
			Protocols:           anthropicProtocols(),
			DefaultEndpoint:     "https://api.anthropic.com",
			FirstPartyClientIDs: []string{"claude"},
		},
		"grok-api": {
			ID:              "grok-api",
			Name:            "xAI Grok API",
			Description:     "xAI Grok API",
			APIStyle:        StyleOpenAI,
			Protocols:       []transport.Protocol{transport.OpenAI},
			DefaultEndpoint: "https://api.x.ai/v1",
		},
		"gemini-api": {
			ID:              "gemini-api",
			Name:            "Google Gemini API",
			Description:     "Google Gemini API (OpenAI-compat mode)",
			APIStyle:        StyleCustom,
			Protocols:       dualProtocols(),
			DefaultEndpoint: "https://generativelanguage.googleapis.com/v1beta/openai",
		},
		"litellm": {
			ID:              "litellm",
			Name:            "LiteLLM",
			Description:     "Self-hosted LiteLLM proxy",
			APIStyle:        StyleCustom,
			Protocols:       dualProtocols(),
			DefaultEndpoint: "http://127.0.0.1:4000",
		},
		"local": {
			ID:              "local",
			Name:            "Local model server",
			Description:     "Ollama / LM Studio / llama.cpp style servers",
			APIStyle:        StyleOpenAI,
			Protocols:       []transport.Protocol{transport.OpenAI},
			DefaultEndpoint: "http://127.0.0.1:11434/v1",
		},
		"custom": {
			ID:              "custom",
			Name:            "Custom gateway",
			Description:     "Bring your own OpenAI/Anthropic-compatible endpoint",
			APIStyle:        StyleCustom,
			Protocols:       dualProtocols(),
			DefaultEndpoint: "",
		},
	}
}

// IDs returns sorted built-in catalog ids.
func IDs() []string {
	out := make([]string, 0, 8)
	for id := range Builtin() {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
