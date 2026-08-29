# ADR-0015: Compatible provider plugins declare endpoints; core owns credentials and proxying (EXT-03)

- Status: accepted (amends [0012](0012-plugin-trust-boundary.md))
- Date: 2026-08-12

## Context

Adding an OpenAI/Anthropic-compatible vendor as a normal `Provider` repeated the
same security-sensitive implementation: distinguish pay-as-you-go and coding
subscription keys, write owner-only snapshots, authenticate a loopback proxy,
translate Codex Responses, preserve Claude Messages, discover models, and rotate
a multi-account pool only after a credential-scoped quota error. Repeating that
inside every plugin makes review harder and lets small endpoint-only extensions
get the proxy boundary subtly wrong.

Subscription products add a second correctness constraint. Kimi Code, GLM
Coding Plan, MiniMax Token Plan, MiMo Token Plan, and Model Studio Coding/Token
Plans issue keys and endpoints that are deliberately not interchangeable with
their pay-as-you-go APIs. Some plans permit interactive coding tools only and
prohibit automated backend use or a falsified client identity. Treating all of
them as a generic API key would route billing incorrectly and could violate the
provider's terms.

The existing plugin context (ADR-0012) could register a full `Provider`, but it
had no safe, shared way to request the framework's compatibility-proxy machinery.
Handing plugins stores or proxy services would widen ambient authority over all
accounts; copying internal imports would create an unsupported ABI.

## Decision

1. **The plugin context adds `registerCompatibleProvider(definition)`.** The input is
   inert data: provider identity, official HTTPS endpoint profiles, credential
   product kinds, endpoint authentication schemes, fallback model ids, and
   narrow quota-error patterns. Core
   validates it and registers both a first-class account provider and catalog
   provider. The context still exposes no store, service, database, or data root.
2. **Credential products are separate input kinds.** `api-key` and
   `subscription-key` create distinct proxy-only account snapshots. Each stored
   credential records its kind and endpoint profile, so a plan key cannot drift
   onto a pay-as-you-go URL after restart. Provider-specific labels and profiles
   remain on the generated provider, not in CLI/TUI switches.
3. **Core owns the compatibility proxy.** It binds loopback only, requires the
   per-instance secret from ADR-0006 on every `/v1/*` route, injects the upstream
   credential only after local authentication, and never returns that credential
   or the local proxy token through status output. Claude Messages uses a native
   Anthropic endpoint when declared and otherwise translates to Chat
   Completions. Codex Responses translates to Chat Completions and emits the
   complete Responses SSE lifecycle.
4. **Client identity is preserved.** The downstream `User-Agent` is forwarded
   unchanged and local `Authorization` / `x-api-key` headers are replaced with
   the chosen upstream credential. The declarative path does not import browser
   cookies, consumer web sessions, or undocumented OAuth tokens and does not
   impersonate a provider-approved tool.
5. **Pools use the existing quota guard.** Account order is provider-scoped;
   failover occurs only when the upstream response proves that the credential is
   invalid or its balance/quota is exhausted. Cooldowns and secret-free audit
   events use the same runtime state as built-in OpenCode/Gemini pools.
6. **A reviewed provider pack ships as a bundled plugin.** `plugin add
   bundled:<name>` resolves a directory included in the AnyPick package. It is
   still recorded disabled, whole-package digest pinned, and explicitly enabled
   under ADR-0012/0014. "Bundled" changes discovery ergonomics, not trust.

## Consequences

- A popular provider pack can add Kimi, GLM, MiniMax, MiMo, DeepSeek, and Qwen
  without adding provider-id branches to core, CLI, TUI, or the proxy service.
- A new compatible vendor is mostly a reviewed endpoint/product definition; it
  inherits proxy authentication, protocol translation, pooling, and Hub support.
- Existing plugin API v1 packages remain valid because the context change is
  additive; compatible-provider plugins can use the new registration function
  without invalidating plugins that use only the original three registries.
- Compatible definitions require public HTTPS base URLs. Self-hosted or unusual
  authentication schemes still implement the full `Provider` contract instead
  of weakening the declarative boundary.
- A bundled plugin still executes an ESM entry in-process and therefore keeps the
  full trust warning from ADR-0012. Declarative registration reduces duplicated
  sensitive code; it is not a JavaScript sandbox.

## Rejected alternatives

- **Make the six providers built-ins.** This grows the default credential graph
  and makes every endpoint update a core release, despite the existing plugin
  lifecycle being the requested extension boundary.
- **Give plugins a generic proxy service handle.** It exposes mutable runtime
  authority and account paths beyond the extension's own provider.
- **Treat subscriptions as API-key endpoint aliases.** It loses the billing/use
  distinction vendors explicitly enforce and makes accidental cross-routing
  likely.
- **Import consumer web sessions.** Those flows are undocumented, brittle, and
  materially different from the official keys vendors issue for coding tools.
