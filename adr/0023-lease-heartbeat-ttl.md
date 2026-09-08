# ADR-0023: Lease heartbeat & TTL policy (PROC-02)

- Status: accepted (2026-09-07)
- Task: G0 decision #4 (SYSTEM_ARCHITECTURE_2026-09-07.md §6)
- Amends: [0003](0003-process-lease-ownership.md)

## Context

ADR-0003 made lease ownership verifiable: a spawned process owns a lease only if
it echoes the `instanceId` AnyPick handed it, and a live PID is never proof of
ownership. But nothing kept a *healthy* lease alive, and nothing reclaimed a
*stuck* one. The lease table had no heartbeat and no expiry, so a process that
was alive but wedged (or a supervisor that had lost DB write permission while its
proxy kept serving) would hold its lease forever, and a genuinely dead detached
child lost its lease the instant its parent exited.

Two failure modes were live:

1. A detached child (spawned by a pool member to keep a long lease alive) lost
   its lease when the parent exited, because the lease was keyed to the parent's
   PID/instance, not the child's.
2. `ReapStaleContext` used a PID-only rule: any row whose PID was not currently
   running was reaped, even if the owning instance still echoed correctly. A
   proxy that had merely lost DB write access was reaped mid-serve.

## Decision

1. Heartbeat cadence is `HeartbeatInterval = 15s` and `LeaseExpiry = 2m`. The
   interval is always strictly shorter than the expiry; a guard test fails the
   build if anyone swaps them, because an interval longer than the expiry means a
   lease can expire between two heartbeats with no chance to refresh.
2. `HeartbeatContext(leaseID, instanceID, ownerPID)` performs a **conditional**
   `UPDATE`: it writes a fresh heartbeat timestamp only when the row still belongs
   to the same `instanceId` and `ownerPID`, and returns `sql.ErrNoRows` otherwise.
   A heartbeat for a lease you do not own is a silent no-op, never a hijack.
3. `HeartbeatInstanceContext(instanceID, ownerPID)` lets a **detached child**
   keep a lease alive on its own. The child does not know the lease ID; it
   heartbeats by instance identity, so a child outlives its parent without losing
   the lease.
4. `ReapStaleContext` new rule: **a PID is a veto, never a verdict.** Rows written
   before this policy (no instance/endpoint) keep the old PID-only rule. Rows
   written after it require heartbeat expiry **and** a failed instance echo before
   they are reaped. The instance echo wins over the heartbeat: a proxy that is
   still serving and still echoes correctly is never reclaimed, even if its
   heartbeat is stale because it lost DB write permission.
5. All heartbeat and reap writes run under the coordinator (ADR-0009 / ADR-0011),
   scoped to the provider that owns the live credential file, exactly like every
   other persisted mutation.

## Consequences

- A healthy proxy is never reaped for a stale heartbeat; only a process that is
  both heartbeat-expired and non-echoing is reclaimed. The crash/ownership blast
  radius from ADR-0003 is preserved, not widened.
- A detached child survives its parent and keeps its lease, so long-lived pool
  members no longer flap their leases when the spawner exits.
- The PID check is demoted to a sanity veto, matching reality: PID reuse (ABA) was
  already why ADR-0003 moved ownership to instance echo. A PID match alone no
  longer authorises reclamation.
- Five mutations are killed by tests: heartbeat that skips the owner check;
  reverting to the PID-only rule; dropping the echo override; dropping the
  freshness check; an instance heartbeat that drops `owner_pid`.
