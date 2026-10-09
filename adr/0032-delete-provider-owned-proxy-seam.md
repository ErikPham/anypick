# ADR-0032: The provider-owned proxy seam is deleted, not kept for a future executor

- Status: accepted (2026-10-09)
- Amends: [0021](0021-plugin-host-removed-from-native-builds.md) decision 3,
  [0031](0031-provider-registration-is-in-tree.md) decision 3
- Provenance: `internal/proxy/service_plugin.go` described arbitrary JavaScript
  plugin providers behind a private Node host that ADR-0020 had already deleted,
  and its three copies of that description outlived the honest one in
  `internal/app/plugin_host.go`.

## Context

ADR-0021 decision 3 kept a seam on purpose:

> `proxy.PluginLifecycle` and `internal/proxy/service_plugin.go` survive even
> though no provider currently implements `provider.ProxyStarter`. They are the
> contract a future Go-native declarative executor would implement.

The reasoning was sound: a future executor is plausible, and rediscovering the
ownership duties later would cost more than carrying them now.

What the seam actually cost is measurable, and the decision did not price it.

`service_plugin.go` is 1256 lines, and 849 of them are the forwarded lifecycle:
`startForwardedLocked`, `failForwardedStart`, `settleAbandonedListener`,
`reconcileForwardedOnHostLoss`, `proveTerminatedChildrenDark`, the scoped
ownership record and its child-kill path, `stopForwardedLocked`,
`statusForwarded`, `ValidatePluginStart`, and the scoped-record loader.

None of it is reachable. `ForwardsLifecycle` is a type assertion against
`provider.ProxyStarter`, and no provider in the tree implements it — there is
not one non-test implementation and not one compile-time assertion. The
predicate therefore answers false for every provider, forever, unless someone
writes the executor this seam was reserved for.

That produced three costs the ADR did not anticipate:

1. **The seam was described wrongly, in three places.** `service_plugin.go`
   opened by describing "arbitrary JavaScript plugin providers ... behind the
   private Node host", a runtime removed by ADR-0020. `service.go` repeated it
   twice. The accurate account existed in `app/plugin_host.go`. A reader
   opening the file that implements the seam learned that the engine was a
   plugin host, which it has not been for a year.
2. **Five call sites branched on an unsatisfiable predicate.** `startLocked`,
   `stopLocked`, `StatusContext`, `ProxyLogs` and the start-internal path each
   asked a question with one answer. Every reader of those functions had to
   decide whether the branch was live, and no type said.
3. **The comments rotted.** Nine months of ADR-era edits, and three files still
   said "JS plugins". A comment that is wrong about which runtime exists is
   worse than no comment, because it is evidence.

## Decision

1. **The seam is deleted.** `internal/proxy/service_plugin.go`,
   `internal/app/plugin_proxy.go`, the `ProxyLifecycle` interface and its field
   on `proxy.Service`, and the `provider.ProxyStarter`, `ProxyStopper`,
   `ProxyStatusReporter` and `ProxyLogReader` capability interfaces go with it,
   along with the `ProxyContext`, `ProxyHandle` and `ProxyStatus` types that
   exist only to cross it. The two generation fields
   `ProxyRuntimeState.PluginRuntimeGeneration` and `.PluginRuntimePID` are
   dropped too: `proxy_state` stores a JSON blob, so removing the struct fields
   ends the contract with no schema migration and no rewrite of a user's row.
2. **The five branches collapse** into the built-in engine path, which is the
   only path that exists. `Service.startLocked`, `stopLocked`, `StatusContext`
   and `ProxyLogs` read as the single implementation rather than as a choice.
3. **What is genuinely reusable is kept, as ordinary code.** The log screening
   that `ProxyLogs` and `ScreenProxyLog` use stays: `RedactSecrets`,
   `credentialPattern`, `redactErr` and `accountSecrets` are how the token
   boundary (ADR-0006) is enforced on every log surface the CLI and the desktop
   protocol read. Only the forwarding is removed. Deleting the file whole would
   have removed a live secret-screening path, which is why this is a cut along
   a concern boundary and not a `git rm`.
4. **Two store methods that outlived their caller go too.**
   `AccountStore.ListProxyStates` and `AccountStore.ClearProxyStateAndLeasesContext`
   had no caller left once the seam was gone. They predate the seam rather than
   belonging to it, so they are recorded here as a consequence rather than part
   of decision 1: an exported method with no caller is a maintenance claim with
   nothing behind it. `ClearProxyStateContext` and `ReleaseProxyRuntimeContext`
   both keep their callers and stay.
5. **ADR-0021's other decisions are untouched.** The plugin registry, the
   whole-package digest, `ErrPluginHostRemoved`, and the per-candidate
   `LoadFailure` all remain. A future Go-native executor re-adds a seam; what it
   must re-derive is enumerated in Consequences below, so the knowledge this
   record deletes is written down here first.
6. **`client_state` leaves the schema.** The table was declared owned by
   `internal/client` in the ownership registry while no code read or wrote it —
   not one statement outside the DDL and the registry entry. It is out of the
   baseline schema and out of the registry now.
7. **No migration drops it.** A fresh database never creates it; an existing
   `~/.anypick` keeps the empty table. `DROP TABLE` on a user's data directory
   is irreversible and this table costs nothing, so the deletion is from the
   code, not from anyone's disk. The consequence is asymmetric on purpose, and
   a reader who finds an install that still has the table is looking at a
   pre-1.1 install, not at drift.
8. **ADR-0007 is left as written.** It names `client_state` twice while
   describing what `link` must not do. That is a record of a 2026 decision, and
   editing it to match today's schema would falsify it; this decision is the
   reader's answer instead. A consequence of 0007 that depended on the table
   surviving would need its own record — none did.
9. **No runtime registration hook is added.** ADR-0031 stands: providers
   register in-tree. If a future executor is wanted, it is a new ADR, and it
   needs a way to reach provider code this tree does not have.

ADR-0031 decision 3 stated the opposite of decision 1 here — it recorded, as
current fact, that `proxy.PluginLifecycle` and `service_plugin.go` "stay, as the
contract a future Go-native declarative executor would implement". That sentence
is now false and this record is what makes it false, so the reader of 0031 must
come here. Everything else in ADR-0031 is unaffected.

## Consequences

**Easier:**

- 849 lines of unreachable code and five unsatisfiable branches are gone. The
  proxy lifecycle is one implementation a reader can follow end to end.
- No file in the tree claims AnyPick is a plugin host.
- The ownership duties a future executor must reimplement are enumerated here
  instead of being scattered through a file nobody reads.

**Harder:**

- A future Go-native executor must write `provider.ProxyStarter` again, plus the
  composition adapter, plus `ValidatePluginStart`'s loopback and bare-origin
  checks. What must hold when it does, recorded here so it is not lost:
  - the engine mints the per-instance token and never trusts a returned one
    (ADR-0006);
  - the engine persists `proxy_state` and the lease before spawning;
  - the engine proves ownership through the `/health` instance echo, never
    through a numeric PID (ADR-0003);
  - every bind is loopback-only and asserted before `net.Listen`;
  - termination is proved through the runtime, not an empty timing window.
- The registry and digest survive untouched, so this costs an executor nothing
  it had. It only costs re-derivation of the lifecycle itself.
- This ADR amends ADR-0021 decision 3 and ADR-0031 decision 3. It does not
  supersede either; their other decisions still stand.

## Follow-up

`internal/pubgate` loses its rationale along with the seam: its writer half was
reserved for the executor this record declines to reserve. It is not settled
here — its own header now argues the case on its own reasoning and says what to
do if no writer ever appears. Nothing else is outstanding.