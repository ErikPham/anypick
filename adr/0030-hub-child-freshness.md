# ADR-0030: Hub child freshness by config revision

- Status: accepted (2026-09-21)
- Provenance: a hub child kept serving a config with deleted sources long
  after the config changed, because every start path adopted a healthy child
  without checking what config it serves. Liveness is not freshness.

## Context

Hub children are detached processes holding their config in memory: they
never re-read it. Three start paths adopted running children on liveness
alone — activation's `hubHealthy` endpoint check, `SpawnHubChild`'s pidfile
check, and the CLI's pid check. A config mutation (sources, owners) while a
child runs therefore served stale routing indefinitely, and the failure
looked exactly like the data bug ADR-0029 fixed: `no snapshot` for accounts
that no longer exist anywhere in the database.

Quitting the app does sweep hub children (ADR 0019), but children are
deliberately detached so CLI/TUI sessions keep working — and any mutation
between restarts re-opens the window. Freshness must be checked at adopt
time, not just at quit time.

## Decision

- Hub `/health` carries the config `Revision` the child started with.
  Revisions already bump on every config write; a child predating the field
  reports zero, which never matches.
- `hub.ChildSynced(endpoint, wantRevision)` is the single freshness check;
  `hub.RestartStaleChild` stops a stale child ownership-verified (pidfile
  record plus `/health` instance echo, ADR 0003) so the caller spawns fresh.
  A live process the record does not own is never signalled.
- All three adopt sites enforce it: activation executor, `SpawnHubChild`,
  CLI `proxy hub start`. CLI `source`/`owner` mutations reload a running
  child best-effort (warn, don't fail: the config is saved either way).
- Kernel-assigned ports (endpoint unknown to the starter) keep the old
  adopt behavior and say so in code.

## Consequences

- Config mutations reach serving children within one restart, from every
  surface, without manual `stop`/`start`.
- Stale children self-heal on the next start instead of serving ghosts.
- `/health` gains one integer; no auth or protocol version change.
