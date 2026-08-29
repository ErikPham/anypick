# ADR-0016: Compatible backends route by declared upstream capability (EXT-04)

- Status: accepted (amends [0015](0015-declarative-compatible-provider-plugins.md))
- Date: 2026-08-13

## Context

ADR-0015 established a safe declarative path for compatible providers, but the
first implementation kept four responsibilities in one HTTP server module:
credential loading, upstream authentication, quota failover, and protocol route
translation. That shape worked for six vendors, but expanding the reviewed pack
to inference clouds and coding-plan gateways exposed two problems.

First, some providers implement native OpenAI Responses or Anthropic Messages
while others implement only Chat Completions. Always translating discards
provider features; always forwarding breaks mixed pools and providers without
that route. Second, OpenRouter already has a richer built-in catalog. Requiring
an account backend to register a second catalog under the same id makes a valid
account extension fail atomically for the wrong reason.

## Decision

1. **Credential routing is a backend object.** `CompatibleBackend` owns snapshot
   loading, endpoint resolution, auth-header replacement, quota ordering,
   cooldown failover, upstream timeouts, and secret-safe logs. The HTTP server
   owns only local authentication, route handling, and protocol conversion.
2. **Capabilities are declared per endpoint profile.** A profile may declare
   its OpenAI and Anthropic authentication schemes and whether it implements
   native Responses. The server forwards a native route only when every
   candidate in the selected account/pool supports it; otherwise it uses the
   shared translation path so no pool member is silently excluded.
3. **HTTP boundary helpers are shared but not provider-aware.** Streaming with
   backpressure, abort handling, CORS, JSON decoding, model-list normalization,
   and safe upstream error decoding live in one module with no credential or
   provider registry access.
4. **An account backend may reuse an existing catalog.** Registration always
   adds the account provider. It adds a catalog only when the id is not already
   present, preserving a built-in catalog's richer model policy while enabling
   managed credentials and account pools for the same vendor.
5. **Provider expansion remains inert plugin data.** The reviewed provider pack
   separates model vendors, coding-plan gateways, and inference clouds into
   importable definition modules. Those modules receive no service/store handles
   and execute only declarative registrations through ADR-0015.

## Consequences

- Native Responses providers retain vendor features required by newer Codex
  clients, while Chat-only backends remain usable through translation.
- A mixed pool chooses one route shape for the whole request, so failover never
  changes protocol semantics halfway through the candidate ring.
- Adding providers no longer grows the credential/failover code path or the HTTP
  boundary, and backend behavior can be tested independently of socket routing.
- Capability claims are security- and correctness-sensitive reviewed data. A
  false native claim can break requests, so only vendor-documented endpoints are
  marked native.
- The additive profile fields keep plugin API v1 source-compatible.

## Rejected alternatives

- **Probe capabilities on every request.** A failing probe consumes quota, adds
  latency, and cannot distinguish unsupported routes from transient upstream
  failure reliably.
- **Use the first pool member's capabilities.** Later failover could select a
  candidate that cannot serve the chosen route.
- **Always translate Responses.** It loses native built-in tools, stored response
  semantics, and provider-specific streaming events where the upstream supports
  them correctly.
- **Overwrite an existing catalog from a plugin.** Catalog replacement would let
  a credential extension silently change model policy for gateway users.
