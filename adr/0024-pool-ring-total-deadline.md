# ADR-0024: Single pre-commit deadline budget for a pool ring (POOL-01)

- Status: accepted (2026-09-07)
- Task: G0 decision #5 (that plan was consolidated into docs/history/ARCHITECTURE_AND_ROADMAP_2026-09-08.md)

## Context

A pool failover ring walks an ordered list of members, and each member can stream
a response. The old behaviour gave every attempt its own unbounded time budget and
let a started stream run until the client gave up. Two problems followed:

1. There was no upper bound on how long a *whole* ring could occupy a request.
   The previous `N×60s` per-member rule was arbitrary and let a ring with many
   members tie up a request for minutes while every member slowly timed out in
   turn.
2. An attempt that died for lack of time was reported as a generic upstream
   error, so a client could not tell "the vendor rejected this" from "we gave up
   routing it." Worse, a member that had *already committed response headers*
   could be cut mid-stream if its deadline fired after the stream started.

## Decision

1. `DefaultPoolRingDeadline = 2m` is a **pre-commit** budget for the entire ring,
   not a per-member timeout. Every attempt in the ring receives a context carrying
   the same absolute deadline derived from the moment the request entered the
   ring.
2. When a member commits its response headers, the deadline timer is stopped via
   `captureWriter.onCommit`. A stream that has already begun is never cut by the
   ring deadline — the budget governs *queuing and failover*, not *a stream that
   is already flowing*.
3. An attempt that dies because the budget was exhausted returns
   `504 upstream_timeout`, distinguishing "we ran out of routing budget" from a
   real vendor error. This is a new, honest status, not a faked 5xx that blames
   the upstream.
4. Cancellation is propagated per-attempt through the shared context, so a dead
   member's in-flight request is abandoned as soon as the budget fires, and no
   later attempt waits on a member that can no longer win.

## Consequences

- A ring can no longer hold a request for an unbounded multiple of a per-member
  timeout; the worst case is bounded by the single ring budget.
- A committed stream is never truncated by the deadline, so SSE / long completions
  from a healthy member are safe even if routing took most of the budget.
- Clients receive an explicit `504 upstream_timeout` for budget exhaustion, which
  the desktop/TUI can surface as "routing timed out" rather than "vendor failed."
- Four mutations are killed by tests: dropping the commit-time escape that stops
  the timer; removing the budget entirely; removing per-attempt cancellation
  (caught because response latency would otherwise regress); removing the
  between-attempt budget check.
