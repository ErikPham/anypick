# ADR-0027: Every action gets exactly one attributed outcome (DESK-01)

- Status: accepted (2026-09-18)
- Provenance: an audit of every app action after a real failure — a switch that
  the engine had already applied ran to the host's confirm timeout and reported
  "The service didn't confirm in time" because the host had rejected a frame and
  then terminated the engine.

## Context

The app and the engine speak one stdio protocol: the app holds a single mutation
slot and a handful of per-row spinners, and every one of them can only be
released by a terminal frame that names its request. The audit found that several
paths could answer a request twice, answer nothing, answer without naming the
request, or kill the answer channel outright:

1. A command that died before publishing a result answered with a
   `protocol-error` that carried no `requestId`, so the row that sent it could
   not be settled and spun until the host's timeout.
2. A malformed-but-readable frame was rejected the same way, with the id sitting
   in the very payload that was refused.
3. Synchronous commands (`mutate`, `model-roles`, `backup-inspect`, `logs`) ran
   on the select loop with no recover and no deadline: a provider panic killed
   the engine, and a handler that blocked delayed every later command — including
   the user's cancel — while publishing nothing.
4. A failed post-mutation refresh answered the same request a second time, so a
   success arrived with an error toast on top of it.
5. `account-backup-forget` was validated and implemented but never routed, so the
   app's staged-review discard answered "unknown mutation operation".
6. An unencodable v1 frame was written with an empty body; a strict host treats
   that as a protocol failure and stops the service.
7. The host released its mutation slot for *any* unreadable frame, including
   optional ones (`logs`, `usage-event`, `status`) that carry no request id, and
   then dropped the real result that arrived afterwards.
8. A conflict pick that succeeded never cleared its spinner, because the row had
   no snapshot state to confirm against, and one action (`account-backup-import`)
   could stay pending forever after the service died.

## Decision

1. **One terminal frame per request, always attributed.** `ProtocolError` carries
   an optional `requestId`, filled from the refused payload when it can be read;
   the engine remembers the ids it has already answered (bounded, 256) so a
   recovery path cannot answer twice, and a retried command is reported as
   `STATE_CONFLICT` instead of running its handler again.
2. **A budget and a watchdog on every command that can take seconds.** `mutate`,
   `model-roles`, `backup-inspect` and `logs` run off the select loop with an
   absolute deadline; a watchdog publishes `ACTION_TIMEOUT` (with `mutated=true`
   for writes) if the handler has not answered. The handler still runs to
   completion — cancelling mid-write is what ADR 0024's pre-commit rule forbids —
   but the host is answered on time. Deliberately not part of the in-flight wait:
   a graceful quit must not block on a watchdog.
3. **Panic containment at the command boundary.** `handleSafely` converts a
   provider panic into one attributed terminal frame, so a panicking adapter
   costs one failure and not the process, the proxies it owns, and every
   in-flight request.
4. **A cancel that arrives before its handler is remembered.** Because commands
   run off the loop, the loop can read `cancel` first; a cancel ledger (2 minute
   TTL) is consumed at registration, and a cancelled-before-start login answers
   `cancelled` without launching the provider at all.
5. **Failures after a write say so.** Post-write steps (hub reload, live-login
   clear, quota-guard pool restart) wrap their error with `mutated=true`, so the
   app tells the user the state changed even though the action failed.
6. **Advisory work never sits inside an action's budget.** Catalog warming runs
   in the background and reports through the next snapshot; a failed state
   snapshot is retried by the ticker with a dirty flag instead of leaving the
   host waiting for a change it will never see.
7. **Sign-ins have an engine backstop, and a graceful exit is bounded.** The
   browser launcher bounds itself (10 minutes by default), so the engine's own
   deadline sits just past it (12 minutes) and answers `LOGIN_TIMEOUT` when an
   adapter ignores cancellation; shutdown waits for running work for 10 seconds
   and then exits, logging what was still running, because the host owns the
   outer kill timeout and a wedged adapter must not pin the process.
8. **Per-kind budgets where the host has its own.** `logs` is bounded at 8
   seconds — shorter than the host's 10-second log timeout — so a slow log read
   ends in the engine's attributed failure instead of the host's generic "the
   log request timed out". The backup review keeps 60 seconds (a large archive is
   legitimately slow) and the host waits 30.
9. **The host settles on the frame that owns the row.** A named rejection settles
   the row (or the log/backup slot) it names; an unreadable optional frame
   settles only itself; a success cancels that row's confirm timeout and stops
   the spinner when there is no snapshot state to confirm against; and losing the
   service fails every pending row, names itself in Needs Attention, and clears
   optimistic switches instead of leaving the UI claiming work is still running.

## Consequences

- The failure mode that started this ("the engine did it, the app never heard")
  now requires an engine that cannot write at all; every other path ends in a
  frame the app can attribute, and the frame kinds that can still be unattributed
  (an unparseable command line) are logged with their kind.
- Long actions are slower to appear as "done" than they are to apply: the spinner
  is confirmed by the snapshot, not by the accepted result, which is why the
  confirm budget (75s) now exceeds the engine's own action budget (60s).
- The answered-request window is bounded, so a duplicate older than the last 256
  answers can still run. That is deliberate: an unbounded map would grow for the
  life of a session, and a retry that old has already been reported as lost.
