# AnyPick Go Parity Tracker

Source of truth for Go↔TypeScript parity. Authority contract:
`go/PARITY_PLAN.md`. TypeScript remains behavioral authority. TUI is out of
scope by maintainer decision; TypeScript is never modified from this track.

Status legend: `missing` / `partial` / `complete` / `blocked`.

## GO-PARITY-M0 — Parity contract & fixtures — **partial**

| Feature | TS authority | Go | Status | Validation |
| --- | --- | --- | --- | --- |
| Account envelope fixture + drift test | tests/*, src/core/account-codec.ts | testdata/parity/account-envelope.json, internal/transfer/fixture_test.go | **complete** | go test ./internal/transfer |
| Multi-account backup fixture | account-backup.ts | testdata/parity/multi-account-backup.json (TS-authored), tests/parity/multi-account-backup-go.json (Go-authored); internal/transfer/parity_backup_test.go, tests/parity-backup.test.ts; LIVE drift gate: scripts/parity-drift-gate.ts + TestParityExternalExchange (`pnpm go:parity`) | **partial** | Committed fixtures and live producer/consumer exchange pass locally; `.github/workflows/ci.yml` now wires a dedicated parity job. Keep partial until that new job is observed green on the committed tree; no remote result is inferred from workflow text |
| Tracker inventory | src/** | this file | complete | review |

Known intentional differences (tested):

- Nested presets rejected — same as TS.
- Unsupported capability pairs come from `transportFor` semantics.
- `tui` command / `--tui`: Go prints an explanation that the TUI ships with
  the TypeScript build and exits 0 (plan §Scope).

## GO-PARITY-M1 — Composition root & bootstrap — **complete**

| Feature | Go | Status | Validation |
| --- | --- | --- | --- |
| Service graph in internal/app (accounts, providers, clients, bindings, presets, pools, hub, journal, leases, proxy, runtime, activation deps) | internal/app/app.go | complete | go build/vet |
| createAppReady: `.migrate.lock` (bootstrap-scoped), legacy `.hotplug` migration (VACUUM INTO + atomic swap), schema migrate w/ newer-version refusal, startup journal recovery, stale lease reap (skipped when supervisor live), idempotent Close | internal/app/{app,lifecycle,legacy}.go | complete | go test ./internal/app |
| Detached helpers skip maintenance (`_serve-*`) | app.Options.SkipMaintenance | complete | scripts/smoke.sh (regression found & fixed here) |
| FileLock idempotent release + O_CLOEXEC | internal/lock/lock.go | complete | lock tests |
| Registry seal after plugin activation | internal/app.Ready seals Providers+Clients immediately after activatePlugins commits all registration kinds | complete | go test ./internal/app -run 'TestReadyActivatesEveryPluginRegistrationKind|TestPluginRegistryCommitIsAtomicOnConflict' (late Clients.Register rejected post-seal; conflicted commit leaves no partial registry state) |

## GO-PARITY-M2 — Providers/clients/catalog/models — **mostly complete**

| Feature | Go | Status | Validation |
| --- | --- | --- | --- |
| Clients gemini/kiro/opencode (apply/reset/inspect, env files, JSONC config, restore state) | internal/client/{gemini,kiro,opencode}.go | complete | internal/client/newclients_test.go |
| Static model policies (all 8 vendors) | internal/catalog/models.go | complete | build + discovery tests |
| Live model fetch (OpenAI/Anthropic/Gemini wire shapes, bounded paging) | internal/catalog/modelfetch.go | complete | go test ./internal/discovery |
| Model cache store (provider+endpoint keyed) | internal/modelcache/store.go | complete | discovery tests |
| Discovery fallback chain fresh→live→stale→catalog | internal/discovery/service.go | complete | go test ./internal/discovery |
| Browser login flows / cancellable login | internal/browserlogin (Run/Availability/ResolveBinary port of browser-login.ts), provider.LoginMethodsProvider+Loginer on claude/codex/grok/kiro, AccountService.LoginMethods/Login (stash→login→verify→save→activate w/ checkpoint rollback), desktop account-login handler with cancel-frame cancellation | **complete** | go test ./internal/browserlogin ./internal/accounts -run 'Login' + ./internal/desktop -run TestDesktopAccountLogin |
| Codex credential-store gate (inspectCodexCredentialStore port; DetectLive gate + CODEX_CREDENTIAL_STORE_UNSUPPORTED on login) | internal/provider/codex_credential_store.go | complete | provider tests TestInspectCodexCredentialStore/TestCodexDetectLiveGatedByCredentialStore |
| clearLive parity for grok/codex/opencode/gemini (incl. .env auth-key strip) | internal/provider/clear_live.go | complete | TestClearLiveProvidersRemoveLocalFiles |
| Refresh auth, restore preflight optional interfaces parity | internal/provider | partial (refreshAuth still absent; PreflightRestore present for claude) | provider tests |

## GO-PARITY-M3 — Accounts/profile/config/backups — **partial**

| Feature | Go | Status | Validation |
| --- | --- | --- | --- |
| Single-account export/import envelope (codec validation matrix, staging, atomic write, disabled proxy on import); **identity-check → commit runs under the provider-scoped coordinator lock inside ImportEnvelope** | internal/transfer/{codec,transfer}.go | complete | transfer tests |
| Batch restore under sorted provider-scope coordinator locks (inside RestoreSelected) | internal/transfer/backup.go | complete | TestBackupExportInspectRestoreAtomic |
| Identity collision resolution (`ResolveIdentityTarget`, ACCOUNT_IDENTITY_EXISTS) | internal/accounts/service.go | complete | accounts/transfer tests |
| Batch atomic restore (WriteAccountsBatch in one transaction) | internal/store/account_store.go + transfer/backup.go | complete | TestBackupExportInspectRestoreAtomic |
| Backup export/inspect/selective restore with explicit replacement approval | internal/transfer/backup.go | complete | same |
| Global config / tray prefs / quota-guard settings service | internal/config (Store/ConfigService over `config` table key 'global', TS-shaped JSON + schemaVersion 2; quotaGuardPolicy validation 1..24*60; tray prefs defaults true); app.compose wires Config + ensureBootstrapState runs Ensure under .migrate.lock | **complete** | go test ./internal/config |
| Tray launch-at-login (LaunchAgent install/remove under `tray/login-item` scope) | internal/config/traysettings.go | complete | TestLaunchAtLoginInstallsAndRemovesAgent |

## GO-PARITY-M4 — Bindings/presets/runtime — **partial → link/unlink scope fixed**

| Feature | Go | Status | Validation |
| --- | --- | --- | --- |
| Preset list/show/remove CLI over store (List added) | cmd/anypick/bindings.go, internal/presets | complete | build; store tested |
| `link`/`unlink` project-scoped by default (TS semantics); `link` without --with copies global→project; `unlink [client]` removes project binding(s) | cmd/anypick/bindings.go | **complete** | cmd e2e TestLinkUnlinkAreProjectScoped |
| `reset` clears client config AND the global binding under the client lock (bindingService.reset port) | cmd/anypick/bindings.go | complete | same suite |
| run (ephemeral) with pass-through args + cleanup | cmd/anypick/bindings.go + activation/runtime | partial | runtime tests cover isolation; CLI passthrough not e2e-tested |
| Source-resume history (recordSourceResume/listSourceResumes, preset pointers never retained, corrupt rows skipped, newest-first) | internal/bindings (SourceResume + Record/List), recorded after successful persistent `use` | **complete** | go test ./internal/bindings -run SourceResume |

## GO-PARITY-M5 — Proxy protocol/lifecycle — **partial**

| Feature | Go | Status | Validation |
| --- | --- | --- | --- |
| enable/disable/config/start/stop/status/restart/logs lifecycle | internal/proxy + cmd/anypick | complete for implemented ops | smoke.sh |
| Protocol/SSE streaming parity | internal/protocol | partial | protocol tests |
| Quota guard, retry classification, failover | internal/upstream (ClassifyUpstreamFailure, credential-scoped exhaustion detection with deliberate narrowness, ParseRetryAfter, CooldownRegistry, durable QuotaGuard ordering w/ TS-shaped state file) wired into internal/proxy PoolServer (failover only on 401/402/insufficient-balance/credential-quota; other statuses verbatim; secret-free quota-guard.json under pool-runtime) | **complete** | go test ./internal/upstream -race + go test ./internal/proxy -run TestPool -race |

## GO-PARITY-M6 — Pools & Proxy Hub — **partial → hub complete**

| Feature | Go | Status | Validation |
| --- | --- | --- | --- |
| Pool store service ops (status/enable/disable/member/start/stop) | cmd/anypick/pool.go, internal/pools (+ Store.Set persisting mode/strategy/enabled) | **complete at surface**; disable really disables (regression test) | cmd e2e TestPoolDisableActuallyDisables |
| Pool activation via `pool:<provider>` | internal/proxy service_pool.go + pool_server.go (real credential-ring transport: loopback listener, bearer auth, /health instance echo, member ring via shared upstream resolver, lease keyed (provider,""), executor account-pool branch fills lease/token/port and undoes via StopPoolProxy); strategy persisted but rotation intentionally NOT implemented (TS parity: ordered-failover + active promotion) | **complete** | activation TestExecutePoolActivation* / TestStartManagedProxyBranchesUnchanged / TestPlanAdvertisesEffectivePoolPortAndExecutorBindsIt + cmd e2e TestPoolActivationEndToEnd (client-config endpoint == served port; verified stop releases the (provider,"") lease) |
| Pool token rotation & deterministic port (review round) | src/core/proxy-service.ts startPoolProxy | service_pool.go: prior proxy_state read AFTER stop attempts (verified stop clears → fresh token; surviving record → reuse via pickPoolToken); port = pool.port ?? provider.defaultProxyPort ?? 8080 bound deterministically, bind collision fails loudly (PROXY_START_FAILED), planner advertises the same effective port | TestStartPoolProxyRotatesTokenAcrossConsecutiveStarts / TestPickPoolTokenSurvivingRecordWins / TestStartPoolProxyBindsDeterministicPort / TestPlannedPoolPortDefaults (-race) |
| Provider quotaErrorPatterns in pool failover | backend.ts isExhausted definition branch | CompatibleDefinition.QuotaErrorPatterns threaded through ResolveUpstream into Upstream → PoolServer classifier (first 4000 body bytes, invalid patterns skipped) | TestPoolFailoverOnProviderQuotaErrorPattern (pattern triggers failover; no patterns passes through verbatim) |
| CLI `proxy pool stop`/`disable` verified stop | src/core/proxy-service.ts stopPoolProxy | cmd/anypick/pool.go routes through Service.StopPoolProxy: /health instance-echo veto (ADR 0003 §3), PROXY_STOP_UNVERIFIED-style nonzero failure instead of false "stopped", (provider,"") lease released on verified stop; disable keeps DisableContext but its child-stop is equally verified | cmd e2e TestPoolStopReportsFailureForUnknownPool; lease-empty assertion in TestPoolActivationEndToEnd |
| Hub status/enable/source/owner/preview/start/stop | cmd/anypick/hubcmd.go + internal/hub | **complete** | hub tests + build |
| Hub route lifecycle (token-scoped attach/rotate/revoke, per-model routing, wrong-token 401) | internal/hub/route_lifecycle_test.go: live Server.Start() — pinned model routes to the correct source stub, default source otherwise; re-attach rotates the secret (old 401/new 200); DetachRouteContext revokes immediately | **complete** | TestRouteAttachGrantRotateRevoke (-race) |
| Hub pool-backed sources (`pool:<provider>` account value) with quota-class-only failover | internal/hub/server.go PoolRingResolver + poolSourceProvider; internal/proxy PoolServer.ServeAuthorized (route token already verified by the hub; no second auth) and Service.HubPoolRing (POOL_NOT_ENABLED / POOL_EMPTY fail closed; ring rebuilt per request shares cooldowns through the durable guard state file). TS parity: proxy-hub-server.ts hands every enabled pool member to one backend ring; NO cross-source failover either side. Convention note: TS persists `{kind:'account-pool'}` ref objects while Go keeps the flat essential form in memory — the store boundary adapts on read (ref → `pool:<provider>`) and writes the TS decoder's exact key set back, so rows round-trip both ways | **complete** | TestHubPoolSourceFailsOverQuotaClassOnly / TestHubPoolSourcePassesNonQuotaThroughOnce / TestHubPoolSourceFailsClosedWhenPoolDisabled (-race) |
| Hub config_json cross-runtime round-trip (TS-authored rows decode in Go; corrupt rows fail closed; Go writes satisfy the TS decoder) | internal/hub/store.go decode/marshalTSConfig + store_parity_test.go with testdata/parity/hub-config.json fixture: `{kind:'account',provider,name}` → Source{providerId,account}, `{kind:'account-pool'}` → `pool:<provider>`, enabled flags preserved (absent = enabled); unparseable/wrong-shaped rows error STATE_CONFLICT instead of zero-decoding into an empty config; written shape carries every key src/core/codec/proxy-hub.ts requires (host/revision/modelOwners/sources[].ref/enabled). Known one-field gap: a kernel-assigned port persists as absent (inventing 4680 collides with the managed builtin proxy listener — proven by e2e), so TS reads such a row as unconfigured until a concrete port is set by activation's resolveHubSource; egress credential assertions added to route_lifecycle_test.go on both pool and non-pool branches | **complete** | TestHubConfigRowFromTypeScriptDecodes / TestHubConfigCorruptRowFailsClosed / TestHubWrittenShapeSatisfiesTypeScriptReader / TestHubKernelAssignedPortStaysAbsentNotInvented; hub+proxy -race, cmd suite green |
| Hub route lifecycle e2e | cmd/anypick/e2e_test.go TestHubRouteLifecycleEndToEnd: enable → fixture accounts → `use claude --with hub:route` → fresh per-client token rotated on re-attach; `#N` model pins hit the right source through the real detached child (deterministic offline: stored credentials poisoned with distinct unknown endpoint profiles, DB is authority ADR 0004); wrong token 401; detach under `hub/<name>` mutation scope → same request 401; health-echo-vetoed stop → zero leaked `_serve-hub` children + clean route rows; plaintext token absent from status JSON and all of ANYPICK_HOME except the clients/<id> env mirror | **complete** | cmd suite -count=1 |
| Hub sources activatable (`use claude --with hub:<name>`) | internal/activation: resolveHubSource (deterministic port persisted up front, per-client route token attach, managed-builtin capability), executor startHubProxy spawns detached `_serve-hub` + health gate | **complete** | cmd e2e TestHubActivationEndToEnd |
| Codex live-route sync after Hub changes | — | missing | — |
| Pool member exact mutation + hot reload | cmd/anypick/pool.go requires explicit on/off aliases, changes only the named member while preserving mode/enabled/strategy/host/port/order, validates account existence, and restarts only a verified running shared-pool lease | **partial** | TestPoolMemberCommandPreservesPoolConfiguration / RequiresExplicitValidState / StateMatchesTypeScriptAliases pass; live running-pool restart e2e remains open |
| Quota Guard policy restarts running pools | internal/desktop/handlers.go identifies pools by NULL-account lease, restarts every verified running enabled multi pool, does not restart account-only proxies, and aggregates inspection errors | **complete** | TestRestartRunningPoolsRestartsOnlyPoolLeases / ReturnsInspectionErrors |
| Quota Guard event observability | Durable state/event shape exists in internal/upstream, but desktop activity/snapshot surfacing (`src/core/proxy-service.ts` quotaGuardEvents parity) is absent | **missing** | — |

## GO-PARITY-M7 — Legacy JS plugin compatibility — **frozen partial**

ADR 0020 removes the Node plugin host and arbitrary executable ESM plugins from
the target native and npm product architecture. This section remains as
security/recovery provenance for the migration code that already exists; it is
not a native-release dependency. Do not implement the remaining catalog-policy
or multi-account plugin-pool gaps. NATIVE-M6 owns removal of this runtime from
production composition and release artifacts.

The persisted plugin store, manifest validation, whole-package digest/trust
boundary, explicit enable confirmation, and CLI lifecycle are implemented.
`scripts/plugin-host.mjs` plus `internal/pluginhost` now provide a bounded,
versioned, cancellable private-Node RPC transport with digest-before-import,
strict frames, timeouts, crash handling, atomic activation, retained nested
objects, sanitized stderr, and reverse disposal. The Go composition root starts
the host lazily, commits all four registration kinds before sealing provider and
client registries, reports per-plugin degradation without disabling built-ins,
and disposes the host with the app. The shared fixture registers and exercises
Provider, Client, CatalogProvider, and CompatibleProvider through a real Node↔Go
process boundary; registry-conflict rollback is also covered. Post-activation
host recovery/restart now replaces a crashed, protocol-poisoned, explicitly
terminated, or closed host with exactly one new generation (single-flight),
replaying digest-verified activations onto it through CAS-guarded per-plugin
registry reconcile — see the "GO-PARITY-M7 host recovery/restart slice"
section below.
Arbitrary providers now forward browser-login discovery/execution and live
usage, with a host-owned AbortSignal injected into cancellable login calls.
Arbitrary clients now forward `applyPersistent`, isolated-path discovery, and
isolated-runtime/environment-overlay construction. Retained runtime handles
carry their plugin-owned `cleanup()` back into Go behind an at-most-once
rollback guard, while activation supplies the real source and transport rather
than branching on a built-in client id.
Dynamic host health and startup failures are exposed as redacted structured
diagnostics to both `doctor --json` and the desktop snapshot; the macOS
Overview renders current failures with native `Section`/`Label` controls.

Provider proxy forwarding is real end to end (GO-PARITY-M7 provider-proxy
slice). `provider.ProxyStarter` (+ optional `ProxyStopper`,
`ProxyStatusReporter`, `ProxyLogReader`) mirrors the TS optional methods;
capability comes only from `startProxy` presence on the registered adapter.
`proxy.Service` routes StartProxy/Stop/Status/Logs for forwarding providers
through a composition-wired `PluginLifecycle` while keeping every ownership
duty in core: engine-generated per-instance tokens (ADR 0006) are handed out
but never accepted back, starts adopt only health-echo-verified instances,
every post-invocation failure compensates through the provider's idempotent
stop plus an ownership-echo proof before any record is erased — cancelled
invocations carry a host-level invocation-settled acknowledgement
(pluginhost InvokeSettling), an unsettled verdict TERMINATES the hosting
runtime (a dead process cannot bind) and then runs an active hunt through the
account's scoped proxy.pid record — strict-echo-verified stops plus adoption
of anything that re-binds the recorded identity. NO outcome on that path is
proof: a verified kill of the recorded PID says nothing about a double-
detached grandchild (the engine holds no job/container/escape-proof group
handle over plugin-spawned trees), so the no-handle force path always reports
unproven, keeps the scoped record as future-hunt identity, and keeps a
diagnostic proxy_state row naming the planned endpoint and token instead of
cleaning up blindly. An empty planned socket alone is never proof either,
because a detached child between spawn and bind owns no listener yet. The
scoped record itself is only actionable after full validation against this
invocation (version, positive pid, provider/account, bare loopback endpoint
matching the requested endpoint/handle instance, fresh createdAt) plus two
consecutive health probes whose pid is positive AND equal to the recorded
pid; every verification gap fails closed without signalling. Host-loss
reconciliation for OTHER accounts runs only after the failing account's
mutation locks are released, under each affected account's own sorted scopes
(ADR 0009): it re-reads the persisted instance under lock, requires the echo
to stay absent for a whole verification window (one missed probe proves
nothing about a live detached proxy behind a transient timeout), and only
atomically clears state plus leases when the row carries an engine-owned
plugin-runtime generation stamped after `/health` proved that exact supervised
host PID served the instance and that same generation was terminated and
reaped. Plugin-written `proxy.pid` shape never classifies a listener as
in-host; detached, legacy, invalid-record and otherwise ambiguous rows retain
their state, token and leases fail closed. Every reconciliation error is
propagated. Stops are
verified
through the instance echo and fail closed as `PROXY_STOP_UNVERIFIED`; no
numeric PID is ever signalled or trusted. Plugin endpoints are validated to
bare loopback http(s) URLs (no userinfo/query/fragment that could smuggle a
token into CLI output), and provider-controlled output is secret-screened at
the Service boundary as the final pass after every overlay (`RedactSecrets`:
exact persisted token plus generic bearer/x-api-key/token patterns, bounded
length) across forwarded log text, status fields, and error messages, so no
plugin string can carry the engine secret past CLI or desktop output. The
TS-shaped ProxyContext crosses the RPC
with host-owned AbortSignal injection; plugin-returned handles/status are
validated at the Go boundary (loopback-only endpoints required), and the
desktop `logs` frame is bridged through `Proxy.ProxyLogs` so plugin-owned log
sources reach macOS with history preserved after a stop. The shared fixture
now hosts a token-gated loopback HTTP proxy inside the Node host including
bind-then-throw and cancelled-late-bind modes; regressions live in
internal/proxy/service_plugin_test.go (fake lifecycle against real sockets),
internal/app/plugin_proxy_test.go (full ready.App.Proxy through the real
Node↔Go boundary), internal/desktop/logs_bridge_test.go,
scripts/plugin-host.test.mjs (raw protocol), and cmd e2e suites stay green.
Historical M7 gaps are recorded below but cancelled for the native cutover.

Host recovery/restart is **complete**. Cancelled by ADR 0020: multi-account pool lifecycle
for arbitrary providers — `startPoolProxy`/`stopPoolProxy` still bind the
built-in `_serve-pool` credential-ring server and do NOT forward through
the plugin lifecycle (the pool path needs the pool ctx options (`pool:true`,
`authDirs`, `quotaGuard*`) threaded over RPC plus (provider,"") lease
handling; single-account forwarding is complete and pool behavior for
plugins remains built-in-only); RPC-backed catalog model-policy methods.
These items must not be resumed unless a later accepted ADR restores an
executable extension runtime. NATIVE-M6 instead removes the Node runtime.

## GO-PARITY-M7 host recovery/restart slice (this session)

Post-activation recovery closes the last lifecycle gap: the private Node
runtime is now REPLACEABLE, not a corpse the composition root keeps pointing
at. Design (internal/app/plugin_supervisor.go):

- Triggers: explicit Terminate issued by the unsettled-startProxy
  compensation, unexpected crash, ErrProtocol, or any invocation meeting an
  already closed/degraded host. Doctor/desktop diagnostics stay passive
  observers; they flip degraded → running once any recovery succeeds.
- Single-flight: concurrent losers of one detection block on the winner's
  flight channel and consume its published result; only ONE replacement Node
  process can ever spawn per generation change (`starts` counts real spawns,
  incremented after every abort gate). Two active generations are structurally
  impossible: the old generation is terminated before the replacement spawns.
- Generation identity: every adapter binds (supervisor, generation) at build
  time. A lost-host failure resolves the CURRENT-generation base by registry
  id — stale handles are never replayed against a newer process. Each
  restart re-verifies bytes on the new host (digest-before-import), replays
  specs in discovery order, and **stages** adapters fully. Ownership is
  **per plugin**, not four flat lists: the replacement transaction
  reconciles each plugin's desired set and CAS-removes every
  failed/omitted previous-generation registration (`RecoverRemove`).
  Identity, base maps and every provider/client/catalog swap publish under
  one shared publication write lock (`pubgate.Gate`); Get/Has/IDs/Lookup
  and Identity take the matching read lock, so readers observe all of
  generation A or all of generation B — never a mixed snapshot, and never
  a B value that later rolls back. The write lock is released before
  replacement-host Dispose/Close so built-in lookups are not stalled by
  cleanup. A failed swap rolls back registry, catalogs, per-plugin
  ownership and supervisor identity before the write lock drops. Built-ins
  remain unreachable (CAS against the previously-committed pointer).
  Ownership lookups carry plugin identity: only the owning plugin may
  replace an id. A conflict degrades that candidate and the replacement
  still publishes healthy siblings.
- Invocation semantics (pluginhost.ErrNotDispatched is the pivot): a write
  that accepted **zero** bytes is not-dispatched and may recover+retry; a
  write that accepted **any** byte then failed is `ErrWriteInterrupted`
  (ambiguous — mutations are not replayed; InvokeSettling returns
  settled=false). Encode/size rejection is `ErrLocalFrameRejected` (local,
  not host loss; classified before `ErrProtocol`). Crash-after-dispatch
  recover+retry ONLY for read-only hooks; mutating hooks return
  ErrUncertainMutation. startProxy recovers ONLY on ErrNotDispatched.
  Caller ctx flows through withRecovery / replacementProvider / ensure;
  the restart goroutine is detached so a cancelled waiter returns
  immediately while other joiners still get the host. Restarts reuse the
  startup-resolved NodePath+ScriptPath verbatim.
- Close-vs-recovery: Close takes `spawnMu` **before** setting closing, and
  restart holds `spawnMu` from the closing check through pluginhost.Start.
  Close therefore cannot begin during the Start TOCTOU, and Start cannot
  run after Close has begun. Close then drains the in-flight flight and
  re-reads the current host.
- Terminate-vs-restart: PluginLifecycle.Terminate takes the expected
  PluginRuntimeIdentity captured BEFORE dispatching the failed call;
  TerminateGeneration refuses (fail closed, keeping diagnostic rows) when the
  hosting runtime advanced instead of killing a replacement other accounts
  serve from. In-host proxy stamping uses the engine-owned serving
  generation of the host pointer that actually ran the call
  (`PluginStart.Runtime`, never plugin JSON) AND still requires /health to
  echo that exact PID — a dead generation A is never stamped onto B's
  listener (PID mismatch fail-closes the stamp).
- Per-plugin commit is atomic: RecoverSwap/catalog writes roll back through
  RecoverUndo if a later registration of the same candidate fails, and
  failed/omitted ids of that plugin are CAS-removed rather than left
  pointing at the terminated host.
- Diagnostics stay redacted and structured: recovery failures surface via
  ErrRecoveryUnavailable wrapping the cause plus per-plugin LoadFailure rows
  (startup failures preserved, latest recovery attempt authoritative); no
  plugin output crosses the boundary.

Regression suite (go/internal/app/plugin_recovery_test.go, real Node↔Go):
TestConcurrentRecoveryStartsExactlyOneHost, Test-
RecoveryReactivatesPluginsWithoutDuplicateRegistrations, TestCloseRacing-
RecoveryDoesNotRespawn, TestTerminateOldGenerationCannotKillReplacement,
TestUncertainMutationIsNotReplayed, TestReadOnlyCallMayRecoverOnce,
TestProxyGenerationRegression (explicit StartProxy after host-loss recovers
once, in-host state stamped per serving generation, exact-terminated-
generation cleanup, stale-reference refusal, detached/ambiguous rows keep
failing closed). Interleaving pins:
TestReplacementInvisibleUntilGenerationPublished,
TestRecoveryHonoursCallerCancellation,
TestCloseWaitsForInFlightSpawnPermit,
TestReadersNeverObserveMixedGenerationDuringPublish,
TestFailedReactivationRemovesDeadRegistrations,
TestOmittedRegistrationsAreRemovedOnRecovery,
TestCrossPluginTakeoverDegradesOnlyOffender,
TestPublicationGateReleasedBeforeHostCleanup,
TestOversizedMutationIsLocalRejectionNotRecovery. Host-level write pins:
TestHostZeroByteWriteIsNotDispatched, TestHostPartialWriteIsAmbiguous,
TestHostFullFrameWriteErrorIsAmbiguous, TestHostClosedCallIsNotDispatched,
TestHostProcessIdentityDiffersAcrossRestarts. Registry pins:
TestRegistryRecoverSwapReplacesOwnedAndRefusesBuiltin (provider+client),
TestRegistryRecoverRemoveDeletesOwnedAndRefusesBuiltin.
Host recovery/restart is **complete**; M7 stays **partial** for catalog
model-policy RPC and plugin-provider pool lifecycle.

## GO-PARITY-M8 — Complete CLI — **substantially complete**

Command tree now includes: use/run/current/list/link/unlink/reset/add,
account list/remove/refresh/export/import, gateway add/list/show/edit/remove/reset,
preset list/show/remove, proxy enable/disable/config/start/stop/status/restart/logs
+ pool * + hub *, serve/tray(app)/start/status/stop, doctor/update/completion/
providers/clients, hidden `_serve-proxy/_serve-hub/_serve-pool/__complete?`.
`tui` intentionally explains and exits 0.

This pass added: root-level global flags (`anypick --json providers` works),
`--version/-v`, `tray`, and supervisor `start`/`status`/`stop` with pid-file
ownership; completion now advertises exactly the dispatched command set.
Global-flag matrix now recorded in a `globals` struct (json/dry-run/no-input/
yes/quiet/verbose/trace/reveal/tray) with real behavior wired: `use --dry-run`
plans without persisting (JSON shape mirrors TS publicTransport redaction),
tray start/stop and update honor `--dry-run`, `gateway show` masks the API key
(maskSecret port) unless `--reveal`. Fixed: gateway show/edit/remove/reset were
implemented but never dispatched — the gateway group now exposes all six
Subcommands (regression: cmd e2e TestGatewayShowRedactsUnlessRevealed,
TestUseDryRunPlansWithoutPersisting, TestTrayDryRunDoesNotSpawn).

CLI parity part 1 (NATIVE-M1 cutover slice): structural help/command-tree
golden gate — scripts/parity-cli-tree.ts introspects the TS commander program
(action-free, stub app) into testdata/parity/cli-tree.json;
cmd e2e TestCliTreePathsResolveInGoDispatch probes every non-omitted fixture
path through the real dispatch, TestGoDispatchTreeMatchesTypeScriptTopLevel
extracts run()'s switch cases via AST so no Go-dispatched top-level command can
exist outside the TS tree ∪ documented omissions (tui=plan §Scope,
plugin=M7, supervisor helpers), TestUnknownCommandPrintsUsageAndInvalidUsage
pins usage+InvalidUsage. Stdout purity: TestJSONStdoutPurityMatrix asserts
every machine-readable invocation (--json providers/clients/doctor/current/
proxy status/use --dry-run/status/stop/preset list/account export) prints
exactly ONE JSON value with zero trailing bytes. Verbosity flags now have real
consumers: --quiet suppresses use/add chatter (mutation still persists),
--trace prints [trace] step lines to STDERR only, --verbose gates doctor check
detail like TS (regressions in help_parity_test.go). Dynamic positional
completion: hidden `__complete <providers|clients|gateways|accounts [p]>`
lists live ids from the data root; bash/zsh/fish `completion` scripts shell out
to it at positional depth (TS src/cli/completion.ts shape); smoke.sh has a
completion/tree section.

Divergences found & recorded (not silently fixed):
- Go `current <provider>` reports the provider's active account snapshot while
  TS `current [client]` lists effective client bindings — different semantics;
  only the default-output contract was aligned (both human by default, JSON
  under --json; Go JSON field shapes still differ from TS).
- `list` / `account list` have no --json mode in Go (TS prints one combined
  JSON object); full human-table/JSON port deferred as too large for a slice.
- `proxy status` human output is a minimal port of printProxyStatus (no colors,
  fewer fields); JSON shape unchanged from the pre-existing Go shape.

CLI parity part 2, SLICE A (dry-run breadth + prompt gates): every mutating
command TS honors with `--dry-run` now plans purely in Go — gateway
edit/remove/reset, account remove/export/import, preset remove, link (plan-steps
shape), unlink (read-only removal count via new bindings.CountProjectsContext),
reset, proxy enable/disable/config/start/stop, pool enable/disable/member, and
hub enable/source/owner/stop — each BEFORE any lock.WithMutationLock, service
call, journal row, or process spawn (invariant #3). Dry-run JSON is emitted in
TS key order (`orderedJSON`, JSON.stringify undefined-drops + explicit-null
semantics). confirmDelete ported exactly: `--yes` short-circuits; non-TTY
stdin/stdout refuses CONFIRMATION_REQUIRED at exit code 2 WITHOUT reading stdin
(fixes the old stdin-TTY/`--force` check whose error also lacked an exit code);
gated commands are ONLY account remove and gateway remove — TypeScript has no
gate on preset remove/unlink/reset, so adding one there would diverge from the
behavioral authority. Regressions: cmd e2e TestDryRunBreadthLeavesNothingBehind
(22-command table; DB bytes + $HOME tree byte-equal across each dry-run, journal
row count unchanged — harness control-tested against a real mutation),
TestDryRunJSONShapes (one-value purity + action/extras per command),
TestDryRunProceedsWithMutationLockHeld (dry-run completes while
provider/deepseek scope is held elsewhere; bounded timeout),
TestNonTTYConfirmationGate (exit 2/no mutation/--yes proceeds/--json purity/
CI env). Recorded micro-divergences: hub-enable dry-run omits `port` when none
is persisted (Go deliberately never invents 4680, see GO-PARITY-M6) and pins
start:false (no --start flag yet); proxy start/stop keep their stricter
Go arity (provider+name required) so their extras always carry both ids;
pool-member dry-run derives enabled from the ±member convention instead of an
on|off word; `account refresh --dry-run` (TS-honored) is NOT ported because Go
refreshes all saved accounts by default and the extras mapping would be
invented — tracked below as a remaining gap.

Gaps: `account refresh --dry-run`; per-command
human/JSON default audit beyond the matrix above; `plugin` commands await M7.

## GO-PARITY-M9 — Desktop protocol — **partial → protocol v2 service landed**

Implemented: model-roles re-activation (via binding + PlanActivation/Execute),
backup-inspect on the `backup-preview` frame with staged bytes + digest +
TTL'd inspectionId (TrayBackupInspectionManager port), mutate
account-import/export, account-backup-all, **account-backup-export honoring
the `accounts` selection**, and **account-backup-import via
`inspectionId`+`entryIds`+`overwriteConfirmed` with plan re-verification**.
Result.Payload added to protocol; Mutate carries Accounts / InspectionID /
EntryIDs / OverwriteConfirmed.

Route actions now activate through the persistent planner/executor (the
invokeTrayAction → bindingService.use port): gateway chips and current-route
re-invocation were silent no-op successes; cross-provider app-route chips
mutated the SOURCE provider's active account instead of binding the client.
Account-panel rows keep the account-switch semantic (accounts.use) while
Switch-menu chips get their own action id bound through PlanActivation —
matching TS's two distinct registrations.

Regression tests: TestBackupExportHonoursSelectedAccounts,
TestBackupImportViaInspectionFlow (internal/desktop/backup_test.go);
TestInvokeGatewayRouteActionWritesBinding,
TestInvokeCrossProviderAppRouteBindsClientNotSourceProvider,
TestInvokeCurrentRouteReappliesBinding (internal/desktop/route_actions_test.go).

Gateway refresh now uses the stored profile secret without echoing it across
the desktop boundary (TestDesktopGatewayRefreshUsesStoredSecretWithoutEchoingIt).
Still unsupported/incomplete: snapshot completeness vs TS (activity,
hubSources/hubConflicts, clientModelConfigs and degraded conditions remain
empty/partial) and Codex live-route sync after Hub changes.

Settings mutations are now implemented: `setting-launch-at-login`,
`setting-auto-start-proxies`, `setting-show-quota`, `setting-quota-guard`
(internal/desktop/server.go + settings_test.go), and snapshots read persisted
preferences via internal/config instead of hardcoded defaults.

The app-owned service contract is now protocol v2 while retaining the reviewed
v1 command/snapshot DTOs: hidden `desktop-service --stdio`, mandatory
engine/host hello + ready negotiation, build/schema/capability/non-secret
instance metadata, canonical bounded frames, immediate initial snapshot,
post-mutation/explicit-refresh snapshot events, bounded usage events, and typed
host-effect request/results. The service path removes the legacy 1 s startup
sleep and 2.5 s full-snapshot polling. Go focused tests and a built-binary
handshake/snapshot/quit test pass; macOS also negotiates the bundled helper in
an app smoke. Remaining M9 gates: all-command cross-process matrix,
kill/restart/shutdown race matrix, installed mismatch/degraded UI proof, shared
fake-service fixtures, and target-OS Tauri service smoke.

Production shutdown now delegates to a composition-owned sweep: cancel and
drain in-flight work, stop verified Hub children, stop every saved-account
proxy regardless of its current enabled toggle, then stop pools. The callback
boundary has a regression test; full process-tree lifecycle e2e remains open.

## GO-PARITY-M10 — CI/docs — **partial**

- Historical smoke.sh gates remain recorded below; the latest tracker-only
  audit ran focused desktop/proxy/cmd tests successfully.
- README updated below.
- Version injection: `internal/buildinfo.Version` linked via `-ldflags -X …buildinfo.Version=X.Y.Z`; source builds report `(devel)`. Verified by smoke.sh (`version ldflags injection`, `source build reports devel`).
- CI now wires Linux Go vet/test/race, real `windows-latest` whole-tree Go
  tests, the live TS↔Go parity drift gate, and macOS Xcode tests + bundled
  app-service smoke + universal app/helper inspection. These are workflow
  additions, not evidence of a green remote run. Complete release assembly,
  signing/installers, SBOM/digests and installed smokes remain NATIVE-M8 work.

## Review-log (self-review + maintainer P1 round)

- Pool-transport review round (1×P1 / 4×P2) — all fixed with regression
  coverage:
  - P1 double passthrough body: pool ModifyResponse handed the captured body
    back to ReverseProxy, which replayed it into the same uncommitted capture
    writer — every ≥400 verbatim body reached the client twice. Fixed with an
    empty replay body; exact-bytes regressions for non-quota passthrough and
    last-member quota failure (pool_server_test.go).
  - P2-1 token rotation: prior proxy_state is read AFTER the stop attempts
    like TS, so consecutive restarts rotate the token and only a surviving
    record (no-op/unverified stop) is reused. Tests:
    TestStartPoolProxyRotatesTokenAcrossConsecutiveStarts /
    TestPickPoolTokenSurvivingRecordWins.
  - P2-2 plan/actual port divergence: pools bind the planned/persisted port
    deterministically (pool.port ?? provider.defaultProxyPort ?? 8080,
    collision fails loudly) and the planner advertises exactly that endpoint.
    Tests: TestStartPoolProxyBindsDeterministicPort /
    TestPlanAdvertisesEffectivePoolPortAndExecutorBindsIt; e2e asserts client
    config endpoint == served port.
  - P2-3 CLI false-success stop: `proxy pool stop`/`disable` route through the
    Service (health-instance-echo veto), return PROXY_STOP_UNVERIFIED-style
    nonzero failures, and release the (provider,"") lease on verified stops.
    Tests: TestPoolStopReportsFailureForUnknownPool + lease-empty e2e assert.
  - P2-4 provider quotaErrorPatterns: threaded from CompatibleDefinition
    through ResolveUpstream into the pool classifier (backend.ts parity).
    Test: TestPoolFailoverOnProviderQuotaErrorPattern.
  - Deferred gaps recorded as tracker rows in GO-PARITY-M6: member-toggle
    hot-reload of a running pool, restartRunningPools, quotaGuardEvents.

- External review round (4×P1 / 3×P2) — all fixed with regression coverage:
  - P1 PID-only ownership (detached.go + app.go SpawnHubChild): replaced by
    `internal/ownership` structured ADR-0003 records — fail-closed reads,
    live-argv identity verification, hub stop gated on the /health instance
    echo (`HUB_OWNERSHIP_UNVERIFIED` instead of signalling unverified groups).
  - P1 Hub rollback: activation now holds `hub/<name>` for the whole critical
    section; AttachRoute overwrite compensates via byte-exact row snapshot
    restore (`SnapshotRouteContext`/`RestoreRouteRowContext`); spawned child
    gets instance-verified stop compensation. Tests: hub_rollback_test.go.
  - P1 protocol limits: entryIds 16×64 → TS's 64×16; backup-export cap
    256 → 64; `setting-quota-guard-enabled` → `setting-quota-guard`.
    Boundary tests in protocol_strict_test.go.
  - P1 fixture drift: live producer/consumer gate `pnpm go:parity`
    (scripts/parity-drift-gate.ts + TestParityExternalExchange over temp
    artifacts); multi-account row downgraded to partial until CI-wired.
  - P2 e2e orphan `_serve-proxy`: cleanup registered immediately after the
    persistent activation; sweep confirms no leaked children.
  - P2 link provenance: GlobalBindingUpdatedAt now recorded; test deep-compares
    full Spec JSON (incl. ClientOptions) + provenance timestamp.
  - P2 TTFB benchmark: mean reported once after the loop (ReportMetric
    overwrite bug); perf-baseline.mjs parsing unchanged.

- Native-cutover Milestone 0 (now governed by ADR 0020): added `internal/proxy/baseline_bench_test.go`
  — hermetic loopback benchmarks for additive proxy request overhead and
  streaming first-byte — driven by root `scripts/perf-baseline.mjs`.
  `newUpstreamProbe`/`newTestServer` widened from `*testing.T` to `testing.TB`
  so benchmarks reuse the exact test fixtures.

- Fixed: `.migrate.lock` held for whole process life starved detached children
  of boot (found by smoke) — lock is now bootstrap-scoped; FileLock.Release is
  idempotent; O_CLOEXEC prevents children inheriting flocks.
- Fixed (P1): desktop backup protocol — Mutate now carries `accounts`,
  `inspectionId`, `entryIds`, `overwriteConfirmed`; selective export honors the
  selection; import resolves a staged inspection (digest re-verified, plan
  re-checked) instead of expecting a raw path. Regression tests:
  TestBackupExportHonoursSelectedAccounts / TestBackupImportViaInspectionFlow.
- Fixed (P1): `link`/`unlink` are project-scoped by default (TS semantics);
  `reset` also deletes the global binding. Regression: e2e
  TestLinkUnlinkAreProjectScoped.
- Fixed (P1): `proxy pool disable` calls DisableContext (and stops a running
  pool child); `enable --strategy/--mode` persist for real. Regression: e2e
  TestPoolDisableActuallyDisables.
- Fixed (P1): ImportEnvelope takes the provider-scope lock internally and
  RestoreSelected takes sorted provider scopes internally — coordination no
  longer depends on callers.
- Fixed (P1): Hub sources activate end-to-end (`use claude --with hub:<name>`):
  planner resolves enabled hubs into managed-builtin transports with a
  per-client route token and deterministic persisted port; executor spawns the
  detached `_serve-hub` child and gates on health. Regression: e2e
  TestHubActivationEndToEnd.
- Fixed (P1): root dispatch handles global flags before the subcommand,
  adds `--version`, `tray`, `start`/`status`/`stop`; completion matches the
  dispatched tree. Regressions: e2e TestRootGlobalFlagsBeforeCommand /
  TestVersionFlag / TestSupervisorStatusStop.
- Banned-string sweep clean.
- Fixed (P1 round 2): backup-preview ready result now echoes `requestId`
  (InspectionManager.Inspect takes it) — regression asserts RequestID.
- Fixed (P1 round 2): planning is pure again for Hub sources. Route attach,
  token generation and port assignment moved into executor `startHubProxy`
  under the `hub/<name>` scope with compensations (detach route, restore
  previous port); no shared+client duplicate routes from project-only link;
  planner never writes DB state (invariant #3).
- Fixed (P1 round 2): hub child lifecycle ownership — SpawnHubChild adopts a
  live child via service.pid or spawns + records one; repeated activations
  reuse the running child; `proxy hub stop` can stop activation-spawned
  children; e2e asserts stop actually reaps the process (no leaks; process
  sweep clean).
- Fixed (P1 round 2): supervisor start is real — hidden `_supervise` body
  starts enabled proxies, touches runtime/supervisor/ready, waits on signal,
  stops owned proxies on shutdown; cmdStart polls readiness instead of
  returning after spawn. Regression: TestSupervisorStartStaysAlive (start →
  sleep 2s → status running → stop reaps).
- Fixed (P1 round 2): `link` without --with reapplies the ENTIRE global spec
  through ReapplySpec/ReapplyProv — model id, transport policy, client
  options and `global_binding_snapshot` provenance preserved. Regression:
  TestLinkWithoutWithPreservesGlobalSpec (deep spec equality).
- Verified (P1 round 2): multi-account parity fixture path resolves from the
  test file location (repoRoot) to go/testdata/parity/multi-account-backup.json
  in both read and generation modes; suite green.
- Validation (P1 round 2 final tree): `go vet ./...` clean; `gofmt -l` empty;
  `go test ./... -count=1` all green; `go test -race ./... -count=1` all green;
  `go build ./cmd/anypick` OK; smoke.sh 13/13; process sweep after gates shows
  zero leaked `_serve-hub`/`_supervise`/`_serve-pool` children.
- Fixed (P2): strict protocol boundary — DisallowUnknownFields decode plus
  per-kind validation mirroring TS isTray*Command guards (version/requestId/
  bounded strings, explicit booleans, unique ≤16 entryIds ≤64 chars, exact key
  sets for backup ops, logs lines 1..200). Negative contract tests:
  internal/desktop/protocol_strict_test.go.
- Fixed (P3): account-login cancellation registration was deleted before the
  login goroutine started work — a cancel frame arriving mid-OAuth would have
  been a no-op. Registration now outlives the run and is removed on settle.

## Review round 2 (P1/P2 fixes)

- Fixed (P1): Codex `Backup`/`Restore` now assert the file credential store,
  and codex implements `PreflightRestore` — under keyring/auto storage a
  switch refuses instead of writing an auth.json the real login ignores.
  Regression: TestCodexFileCredentialStoreGatesAllMutations (incl. no-mutation
  assertion on refusal).
- Fixed (P1): `Store.Read` preserves the complete stored config document
  (defaults.proxyHost, activeProfile, schemaVersion) — a settings toggle can no
  longer erase fields it does not interpret. Regression:
  TestSettingsTogglePreservesWholeDocument (raw stored shape asserted too).
- Fixed (P1): launch-at-login is a typed host effect (ADR 0020 §7). The engine
  emits a `launch-at-login` effect request in Result.Payload; the native app
  executes SMAppService. Direct LaunchAgent plist mutation removed (also fixes
  the invalid duplicated-executable argv and Linux ~/Library creation);
  snapshot launchAtLogin now flows only from Deps.HostState. Regressions:
  TestSettingLaunchAtLoginIsTypedHostEffect (decoy dir proves no OS write),
  TestLaunchAtLoginEffectRequestShape.
- Fixed (P1): browser-login cancellation/timeout escalation — SIGTERM is
  followed by a TerminationGrace window then group SIGKILL, so a launcher that
  traps TERM cannot hold the provider lock forever. Regressions:
  TestRunForcesKillWhenLauncherIgnoresTerm / …Timeout….
- Fixed (P1): late-success cancellation race in AccountService.Login —
  commit barriers after the launcher returns, after live detection, and right
  before SetActive; any cancel rolls the checkpoint back and yields
  PROVIDER_LOGIN_CANCELLED. A cancelled context also upgrades launcher failures
  to cancellation (exactly-once terminal result). Deterministic regression:
  TestLoginCancelInCommitWindowRollsBack.
- Fixed (P2): Retry-After semantics match TS max(1000, retryAfter ?? cooldown)
  — present sub-second values clamp to 1s instead of falling back to the
  configured hour-long cooldown. Table regression: TestGuardRetryAfterClampSemantics.
- Fixed (P2): global flags are recognized anywhere before pass-through `--`
  (`use x --with y --dry-run` works); hidden `_serve-*` helpers keep argv
  byte-exact. Regressions: TestGlobalFlagsAfterSubcommand /
  TestNormalizeGlobalFlagsPreservesPassThrough.
- Fixed (P2): bare invocation consumes persisted ui.defaultSurface via the
  config service — tray opens the app surface, tui explains and exits 0,
  unconfigured keeps the explicit help policy. Regression:
  TestBareInvocationHonoursConfiguredSurface; `tray start|stop` now dispatch to
  the supervisor lifecycle like TS.

Tracked for native-cutover M5 (out of current plan scope per reviewer):
internal/browserlogin process handling needs build-tagged files
(syscall.Access / SysProcAttr.Setpgid / syscall.Kill) for the Tauri Windows
target.

## GO-PARITY-M7 provider-proxy slice (this session)

- Ownership: `proxy.Service` (not the plugin) generates and persists the
  per-instance token; `provider.ProxyHandle.Token/LeaseID` returned by a
  plugin are zeroed at the RPC adapter before they can spread, and
  `proxy.PluginStart` structurally has no token field. Regression:
  TestForwardedStartPersistsEngineOwnedLifecycle + token-rotation test.
- Idempotency/adoption: forwarded starts reuse only a proxy_state record
  whose /health echo still matches its instance id; a second start never
  crosses the boundary again (fake counts calls; app e2e counts fixture
  events). No numeric PID is signalled anywhere on this path — termination
  is requested over RPC and verified by the echo disappearing.
- Fail-closed stop: a listener that keeps serving after the plugin reports
  success, or a start-capable plugin without stopProxy while its instance is
  live, yields PROXY_STOP_UNVERIFIED and KEEPS state+leases for diagnosis;
  stale records (echo gone) are dropped like the built-in path.
- Compensation: failed validation (unsafe endpoint), failed health gate,
  failed state write, lease failure, cancellation mid-start — each restores
  the exact prior config and leaves no partial records; a bound listener is
  compensated via a verified stop first.
- Intentional strictness vs TS: TS's generic pid-file fallback for plugins
  without stopProxy cannot exist here (plugins own their listeners; killing
  recorded PIDs would risk the shared Node host), so an alive-but-unstoppable
  instance fails closed instead of no-op'ing. Tested both branches.
- Pool lifecycle deliberately NOT routed: see the M7 "Still open" note —
  single-account parity is complete; pools keep the built-in engine path.

## Review fixes (external P1/P2 round on the provider-proxy slice)

- Fixed (P1) orphaned listeners: a plugin can bind successfully and then
  reject, and a cancelled call returns before the host coroutine settles — so
  every post-invocation start failure now compensates through the provider's
  idempotent StopProxy (non-cancelled context) plus an ownership-echo proof
  BEFORE any record is erased. A bounded grace window catches late binds after
  cancellation (the planned port was picked free, so occupancy there is ours);
  death that cannot be proven keeps a diagnostic proxy_state row (endpoint,
  instance when known, engine token) instead of cleaning up blindly.
  Regressions: TestForwardedBindThenThrowIsCompensated,
  TestForwardedCancelledStartStopsLateBindingListener,
  TestForwardedUnverifiableOrphanKeepsDiagnosticRecord (real sockets) and
  TestReadyCompensatesBindThenThrowAcrossHostBoundary /
  TestReadyCompensatesCancelledLateBindAcrossHostBoundary (real Node↔Go
  boundary with new fixture modes `bind-throw` / `bind-cancel-late`).
- Fixed (P1) secret screening: provider-controlled text crossed output
  boundaries verbatim even though plugins receive the engine token. The
  Service now screens forwarded log text, status fields
  (endpoint/compatibility/logPath/detail), and error messages through
  RedactSecrets (exact persisted token + generic bearer/x-api-key/token
  assignment patterns) and bounds log output. Sentinel errors like
  context.Canceled pass through unmodified so errors.Is keeps working.
  Regressions: TestForwardedOutputBoundariesRedactEngineSecrets plus the
  redaction assertions in the desktop bridge test.
- Fixed (P2) desktop log bridge: the native-app `logs` frame read only the
  engine's conventional proxy.log file, so plugin-owned sources rendered empty
  in SwiftUI and history vanished once stopped. handleLogs now routes through
  Proxy.ProxyLogs (provider readProxyLogs → engine file fallback), returns
  already-screened text, and frames history for stopped proxies.
  Regression: internal/desktop/logs_bridge_test.go
  (TestHandleLogsBridgesProviderOwnedLogSource); existing handleLogs tests
  updated for the ctx signature without weakening assertions.

## Review fixes round 2 (independent verification)

- Fixed (P1) grace-window-as-proof: an empty socket at the end of a fixed
  window never proved a cancelled coroutine had settled — a plugin binding
  after the deadline would create exactly the untracked orphan the path
  exists to prevent. Two-part fix: (1) pluginhost gains InvokeSettling —
  after writing the cancel frame the pending call stays open until the host
  emits its terminal response, giving StartProxy a real invocation-settled
  acknowledgement; adapters surface proxy.ErrUnsettledInvocation when even
  that budget expires; (2) on that verdict the Service STOPS TRUSTING timing:
  PluginLifecycle.Terminate kills the hosting runtime (a dead process cannot
  bind), darkness is proven through echo or planned-socket probing, and only
  proven death clears records — failed termination keeps the diagnostic row
  and fails PROXY_STOP_UNVERIFIED. Regressions:
  TestForwardedUnsettledCancellationTerminatesRuntime (bind scheduled past
  the grace deadline is prevented by termination),
  TestForwardedUnsettledWithFailedTerminationKeepsRecord,
  TestValidatePluginStartRejectsCredentialBearingURLs,
  TestForwardedStatusScreensAfterRuntimeOverlay. Host restart after
  termination is the GO-PARITY-M7 host recovery/restart slice below
  (degraded diagnostics covered the interim).
- Fixed (P1) endpoint credential components: ValidatePluginStart now rejects
  userinfo, query, fragment and force-query outright — a successful start's
  endpoint (returned through StartResult and printed by CLI surfaces) is a
  bare loopback http(s) URL, so nothing token-bearing can be persisted or
  published. Status screening moved to the LAST step before returning: the
  runtime-state overlay previously reintroduced raw stored endpoint text
  after the earlier redaction pass; regressions pin both.
- Fixed (P2) vacuous listener assertions: two tests read back the account
  config port AFTER compensation restored Port=0 and dialled port zero —
  always dead, proving nothing. Both now capture req.Config.Port inside
  fake.start and probe that concrete port.

## Review round 3 (P1/P2 fixes)

- Fixed (P1): desktop route actions bypassed the planner. Gateway app-route
  chips and selected current-route actions returned success without mutating
  anything (false success); cross-provider app-route chips called
  accounts.Use on the SOURCE provider — a wrong-scope mutation that flipped
  another provider's active account and rewrote its live auth. All non-proxy
  route handlers now run activation.PlanActivation(ModePersistent)/Execute
  (internal/desktop/snapshot.go routeActivator). Account rows keep the
  account-switch operation; Switch chips got a separate action id. A latent
  id-collision bug surfaced by the new tests (two actionID() calls before
  registry insert derived the same id from len(registry)) is fixed with a
  monotonic counter. Regressions: internal/desktop/route_actions_test.go
  (three tests; fail on the pre-fix tree).
- Fixed (P1): planner gated StepWriteNativeAuth behind capability == direct,
  diverging from TS WriteNativeAuth which runs for every persistent account
  source whose adapter declares requiresNativeAuthWrite regardless of
  transport. `use claude --with opencode/x` left opencode's live login stale
  in Go while TS restores it (with durable checkpoint + rollback). The gate
  is now mode+kind+capability-flag only; compatible vendors stay excluded via
  RequiresNativeAuthWrite=false. Regression:
  TestPlannerManagedProxyAccountStillWritesNativeAuth (also asserts the
  RestoreNativeAuth rollback companion step).

## Validation log (this session)

- GO-PARITY-M7 host recovery/restart owner-isolation round (2026-08-26):
  conflicts degrade only the offending plugin (owner-aware id lookup, no
  cross-plugin takeover); publication write lock drops before replacement
  host Dispose/Close. `gofmt -l` clean; `go vet ./...` clean; `go test
  ./... -count=1` all packages green; `go test -race ./internal/pluginhost
  ./internal/app ./internal/proxy -count=1` green; `node --test
  scripts/plugin-host.test.mjs` 6/6; `pnpm macos:design:check` compiled;
  `git diff --check` clean. Host recovery is **complete**. M7 remains
  **partial** (catalog model-policy RPC + plugin pool lifecycle); not
  release-ready.

- GO-PARITY-M7 host recovery/restart slice (2026-08-25): `gofmt -l` clean on
  touched Go files; `go vet ./...` clean; `go test ./... -count=1` all
  packages green; `go test -race ./internal/pluginhost ./internal/app
  ./internal/proxy -count=1` green; `node --test scripts/plugin-host.test.mjs`
  6/6; `pnpm lint` 0 errors (pre-existing warnings only); `pnpm typecheck`
  clean; `pnpm release:check` 1.0.0 synchronized; `pnpm npm:launcher:test`
  PASSED; `pnpm test` 111 files / 1073 tests; `pnpm macos:design:check`
  compiled; `git diff --check` clean; eight `macos/MenuBar*.swift` files
  byte-identical to `dist/macos`. `pnpm check` was not used as the green
  gate because `tests/opencode-proxy.test.ts` is a pre-existing user format
  deviation and was left untouched. M7 remains **partial** (catalog
  model-policy RPC + plugin-provider pool lifecycle); this slice is not
  release-ready.

- GO-PARITY-M7 provider-proxy slice + review fixes rounds 1–3 (2026-08-25):
  `node --test scripts/plugin-host.test.mjs` 6/6; focused
  proxy/app/pluginhost/desktop suites `-count=1` green; `GOTOOLCHAIN=auto go
  test ./... -count=1` all 34 packages green; `-race` green for proxy,
  pluginhost, app and desktop; `go vet ./...` clean; `gofmt` clean on touched
  files; oxfmt/oxlint/tsc/vitest pass on the touched tree (vitest 1073 tests;
  the one remaining oxfmt finding is a pre-existing user modification in
  tests/opencode-proxy.test.ts, untouched by this slice); `git diff --check`
  clean; all eight macos/MenuBar*.swift byte-identical to dist/macos.

- Native-cutover tracker audit (2026-08-24):
  `go test ./internal/desktop ./internal/proxy ./cmd/anypick -count=1` passed;
  `GOOS=windows GOARCH=amd64 go test -exec /usr/bin/true ./... -count=1`
  compiled the complete Go test tree (compile-only, not Windows execution);
  Xcode AnyPick tests passed 5/5, the built app-owned service smoke passed, and
  Release app/helper both reported `x86_64 arm64`. CI jobs are wired but no
  remote-green result is claimed.

- Slice A final tree: gofmt -l . clean; go vet ./... clean;
  go test ./cmd/... -count=1 -race green (~73s); go test ./... -count=1 green;
  scripts/smoke.sh 27/27 pass. cli-tree.json fixture untouched (no TS change,
  no new commands; flags were already registered globals).

- gofmt -l . → clean; go vet ./... → clean; go test ./... -count=1 → all green;
  go test -race ./... -count=1 → all green; go build ./cmd/anypick → OK;
  scripts/smoke.sh → 13/13 pass; pnpm check → 1062 TS tests pass;
  pnpm macos:design:check → SwiftUI compile OK; pnpm tray:check → cargo check OK;
  cd docs && pnpm build → complete; git diff --check → clean (after trimming a
  trailing blank line at README.md EOF, content otherwise untouched).

- Validation (review round 3 final tree): gofmt -l . → clean; go vet ./... →
  clean; go test ./... -count=1 → 32 packages green; go test -race ./... -count=1 →
  32 packages green; scripts/smoke.sh → 13/13 pass.
