# ADR-0028: Provider-scoped model-role defaults with per-connection pins

- Status: accepted (2026-09-19)
- Provenance: switching accounts wiped model choices — roles lived only on
  the binding, so every account of a provider had to be configured separately.

## Context

Model roles (`default`, plus tier slots like `sonnet`/`opus`/`haiku` on
Claude) were stored per binding (`ClientOptions["modelRoles"]`), i.e. per
app↔connection. A plain route switch plans a fresh binding with no roles, so
a user with three Claude accounts configured the same models three times —
and a user who switched often never had stable choices at all. The planner
even documented the stance: "Model selection belongs to the selected source,
not to the client or to the provider's static defaults."

## Decision

Keep per-connection pins, but add provider-scoped defaults underneath them:

- New `provider_model_roles` table (owned by `internal/bindings`), keyed by
  `ModelRolesProviderKey` (accounts by provider id, gateways/hubs by their
  own namespaced identity). Empty write deletes the row.
- Effective roles resolve explicit-pins → provider-defaults (filtered to the
  client's declared roles and the connection's catalog) → engine default.
  The catalog filter is skipped only when discovery returned nothing,
  mirroring `routeDefaultModel`'s leniency — dropping inherited pins on
  every service restart would be worse than an unwitnessed model.
- The planner copies provider defaults into fresh bindings (marker
  `"provider"` in `ClientOptions["modelRolesSource"]`) only when the request
  carries no explicit roles; explicit requests stamp `"explicit"`. Preset and
  bootstrap branches are untouched, and re-apply reuses the stored spec, so
  nothing silently rewrites a live connection.
- `model-roles` gains `scope`: `"connection"` pins (default, historical),
  `"provider"` stores defaults and re-resolves the current connection
  against them, empty roles on connection scope clears pins (reset). The
  desktop publishes the marker as `modelRolesSource` so the editor can show
  provenance, pick the scope default, and offer Reset only for real
  customizations. Legacy bindings without a marker read as explicit.

## Consequences

- Switching accounts inherits the provider default; customizing one
  connection still wins for exactly that connection.
- Changing a provider default does not rewrite already-bound connections —
  they re-resolve on the next switch. Silent rewrites stay forbidden.
- CLI/TUI inherit the behavior through the planner with no frontend changes;
  only the desktop editor exposes scope and Reset.
