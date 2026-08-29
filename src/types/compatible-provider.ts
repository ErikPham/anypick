export type CompatibleAuthScheme = 'bearer' | 'x-api-key' | 'api-key';

/** One official endpoint profile accepted by a compatible provider. */
export interface CompatibleEndpointProfile {
  /** Stable value stored with the account, e.g. `global` or `coding-china`. */
  id: string;
  /** Human label shown beside the profile value. */
  label?: string;
  /** OpenAI Chat Completions base URL (without `/chat/completions`). */
  openaiBase: string;
  /** Upstream OpenAI credential header. Defaults to `Authorization: Bearer`. */
  openaiAuth?: CompatibleAuthScheme;
  /** Forward `/responses` without translation when the upstream implements it. */
  nativeResponses?: boolean;
  /** Anthropic base URL (without `/v1/messages`). Omit to translate through OpenAI. */
  anthropicBase?: string;
  /** Upstream Anthropic credential header. Defaults to the Anthropic SDK's `x-api-key`. */
  anthropicAuth?: CompatibleAuthScheme;
}

/** A secret product sold by the provider, such as pay-as-you-go or a coding plan. */
export interface CompatibleCredentialDefinition {
  /** Provider-declared input kind, e.g. `api-key` or `subscription-key`. */
  kind: string;
  /** Product name shown in credential forms. */
  label: string;
  /** Non-secret product taxonomy surfaced by the UI. Defaults to API key. */
  credentialClass?: 'provider_api_key' | 'coding_plan_key' | 'external_oauth';
  /** Official endpoint profiles usable with this product's key. */
  profiles: readonly CompatibleEndpointProfile[];
}

/**
 * Declarative provider definition for official OpenAI/Anthropic-compatible APIs.
 *
 * Core turns this inert data into a full Provider: secret-boundary snapshot,
 * dual-protocol loopback proxy, pool failover, model discovery, and Proxy Hub
 * backend. A plugin therefore does not need access to AnyPick stores or proxy
 * internals merely to add another vendor endpoint.
 */
export interface CompatibleProviderDefinition {
  id: string;
  name: string;
  shortName?: string;
  description: string;
  /** Preferred local proxy port. Core still allocates another when occupied. */
  defaultProxyPort?: number;
  /** Official model ids used when `/models` is unavailable. */
  models: readonly string[];
  credentials: readonly CompatibleCredentialDefinition[];
  /** Provider-specific phrases that prove a 403/429 is this key's exhausted quota. */
  quotaErrorPatterns?: readonly string[];
}
