# ADR-0026: A pool ring rotates, and a fully cooled ring still serves (POOL-02)

- Status: accepted (2026-09-18)
- Amends: [0024](0024-pool-ring-total-deadline.md) (the deadline budget is
  unchanged; the ring now has two starting orders and a last-resort member)

## Context

Two pool behaviours were inherited from the TypeScript build and both were
surprising to the user who paid for several subscriptions:

1. `strategy` was persisted, validated by the CLI, shown in `pool status` — and
   never used. `round-robin` behaved exactly like `failover`, because the ring
   was always ordered by the quota guard's `Ordered()`: cooling members dropped,
   the last active account promoted to the head. A user picking the strategy the
   CLI documents as rotating received a sticky ring with no signal that the
   choice had been discarded.
2. When every member was cooling, the ring refused the request with a local
   `429 "All accounts are cooling down."` The client was told nothing about what
   the vendor said — how long to wait, whether the subscription was out of
   credit — even though the vendor's own answer was one request away.

## Decision

1. `round-robin` is implemented: the ring's starting member advances per request
   through a per-pool cursor, so successive requests spread across the members.
   `failover` remains the sticky order (`Ordered()`), because promoting the
   account that last worked is what makes failover cheap.
2. Rotation reads a distinct selector, `Guard.Eligible()`: the cooling filter of
   `Ordered()` **without** the active-account promotion. Rotation built on
   `Ordered()` would advance the cursor and still serve the same account, which
   is the bug this ADR removes.
3. Strategy changes where an attempt *starts*; it never changes what happens
   after a failure. A quota-class failure still fails over to the next member in
   the ring, and a non-quota status is still passed through verbatim.
4. A new guard option, `ServeWhenAllCooling` (config
   `ui.quotaGuard.serveWhenAllCooling`, default **true**), replaces the local
   refusal when every member is cooling: the ring attempts the member that
   recovers soonest (`Guard.Cooling()`, soonest-expiry first) and passes the
   vendor's verdict through. Exactly one attempt is made — a full ring walk over
   a throttled provider would multiply load precisely when the vendor asked for
   less.
5. The Hub has one **global** mode, `strategy` (`""`/`failover` default,
   `round-robin`), and per-account switches are the whole membership rule. The
   hub resolves a request to a source as it always did (recorded owner, then pin,
   then the default source) and, when the mode is on, spreads it across every
   *enabled account that can serve the requested model*: the provider catalogs
   decide capability, so two providers advertising the same model id both take
   turns, and one provider's accounts are interchangeable for its own models.
   With no catalog evidence the candidate set narrows to the resolved source's
   provider, which is the same assumption the pool ring is built on.
6. A **pooled hub source** (`pool:<provider>`) is retired as a hub construct: one
   source per provider means a pool row replaces every account switch for that
   provider with a single ring, which is the opposite of per-account state. The
   engine never creates one; a legacy row is shown only so it can be removed, and
   re-enabling it is refused with the two ways forward (switch accounts on
   individually, or turn on rotation). Pool rows themselves remain a CLI feature
   for a shared pool proxy, and the hub leaves them alone.
7. The mode is a property of the request, not of a source list, so it needs no
   pool row, no source surgery, and no second place to look when the answer to
   "which accounts rotate?" is "whichever ones could serve this request".
8. The hub keeps its **own** quota guard (`internal/hub`, keyed by source — a
   request can span providers, so a provider-keyed guard would cool an account
   that was never refused). A credential-attributed refusal cools that account
   and the request moves to the next capable one; a refusal that does not blame
   the credential is passed through verbatim rather than spending another
   account's quota. The state file sits in the hub's runtime directory, so a hub
   restart does not immediately re-hammer an account. Pool proxies keep their
   existing guard and are untouched by this.

## Consequences

- `--strategy round-robin` now means what the CLI and docs say. The pool server
  comment that documented the parity gap ("recorded for diagnostics only") is
  gone, and `TestPoolRoundRobinRotatesPerRequest` fails if rotation is ever
  removed silently again.
- A rotating ring keeps the quota guard honest: the cursor skips cooling members
  rather than resetting their cooldown, and rotation does not resurrect an
  account the guard retired.
- The hub mode is persisted in the hub config (`strategy`) and read at start,
  like the source list: applying it restarts a running hub (`hubReload`), because
  a config write alone leaves the running manifest untouched. The same helper now
  applies source toggles, which previously changed the config while the running
  hub kept serving the old routes.
- Because an all-cooling ring now serves, a client can receive the vendor's own
  `429`/`402` instead of AnyPick's synthetic one. That is the point: the local
  refusal claimed knowledge ("all accounts are cooling") while withholding the
  only fact that helps the caller, the vendor's retry hint.
- `serveWhenAllCooling: false` restores the previous refusal for anyone who
  prefers it, and the guard's zero value in tests still refuses, so the existing
  contract test for the refusal keeps its meaning.
- Turning rotation on in the Hub restarts a running hub, because the route
  manifest (which source index each model is pinned to) is compiled at start. A
  config write alone would leave a switch that appears on while requests keep
  using the old routes.
