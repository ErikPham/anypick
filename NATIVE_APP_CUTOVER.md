# NATIVE_APP_CUTOVER.md — persistent cutover tracker

Durable source of truth for the native application cutover (ADR 0020, which
supersedes ADR 0019). This
file is authoritative across Pi sessions and context compaction: reread it and
the repository before editing anything. Statuses: `missing` / `partial` /
`complete` / `blocked`.

A row is **complete** only when the behavior is reachable through the shipped
surface, focused tests pass, the relevant end-to-end test passes, and the
artifact contains the required files. A package, type, mock, command name, or
isolated unit test alone is not completion.

Companion tracker: `go/PARITY.md` (Go↔TypeScript parity is a dependency of
every native milestone below). Contract: `go/PARITY_PLAN.md`. Architecture:
`adr/0020-node-free-native-app-distribution.md`.

## Current architecture — ownership inversion points (audited 2026-08-24)

| # | Inversion | Authority (current code) | Required direction |
| --- | --- | --- | --- |
| 1 | Node supervisor compiles Swift at runtime and spawns/owns the legacy macOS path | Legacy remains in `src/tray/supervisor-native.ts` (`xcrun swiftc`) and `src/tray/supervisor.ts`; the parallel first-class path is now `src/macos/AnyPick.xcodeproj` + `GoServiceProcess.swift` | Xcode-built app ownership is implemented and smoke-tested; retire the legacy runtime-compile launch path before cutover |
| 2 | Go `app run` can still spawn the native app through `ANYPICK_APP_BIN` | Compatibility path remains in `go/cmd/anypick/main.go` (`cmdAppRun`); production candidates now launch hidden `desktop-service --stdio` from macOS `GoServiceProcess` and the Tauri sidecar | App-owned direction is implemented on both native shells; remove the Go-owned compatibility path only after packaging/install gates pass |
| 3 | Tauri used to read its parent stdin / write commands to stdout | Production `src/tray/tauri/src-tauri/{src/sidecar.rs,src/protocol.rs}` resolves `externalBin`, launches the Go sidecar, negotiates protocol v2, and owns restart/quit; parent stdio remains only in explicit fixture/probe modes | Ownership inversion is removed in code; target-OS bundle/install smoke remains open |

Desktop service today: `buildinfo.ProtocolVersion = 2` and
`desktop.ServiceProtocolVersion = 2`. The app-owned path emits a mandatory
hello/ready handshake (engine/app/schema versions, capabilities, non-secret
instance id, frame bound), then the reviewed v1 command/snapshot DTOs over
canonical base64 newline frames. It sends an initial snapshot, post-mutation
snapshot events, bounded usage events, explicit refreshes, typed host effects,
and has no fixed startup sleep/full-snapshot polling. The legacy `app run`
compatibility bridge still uses protocol DTO v1, the fixed 1 s sleep, and 2.5 s
snapshot polling; its existence is a cutover cleanup item, not the current
service contract.

## Platform matrix (first supported release)

| Platform | Target | Evidence gate |
| --- | --- | --- |
| macOS | 14+ universal (arm64 + x86_64), signed & notarized DMG, Cask | Installed-app smoke on a clean account with dev tools absent from PATH |
| Windows | 10/11 x64, signed NSIS (+ WinGet manifest) | Installed smoke on real Windows CI/runner |
| Linux | x64, minimum baseline Ubuntu 22.04 LTS-class (glibc ≥ 2.35, WebKitGTK per Tauri v2 requirements), AppImage/.deb/.rpm | Installed smoke on oldest supported base |
| npm | Node ≥ 22.5, platform binary packages via optionalDependencies | `npm install -g` + `npm exec` smoke per platform |

Arm64 Windows/Linux: explicitly out of scope until genuinely tested.

## Reference performance measurements (2026-08, Apple M3 Pro, macOS 26.2, go1.21.13)

Reproduce with `node scripts/perf-baseline.mjs` (report → `dist/perf-baseline.md`).
These are local reference points, not promises; re-measure in CI/reference hardware.

| Metric | Measured | Budget (M4) |
| --- | --- | --- |
| Warm Go CLI read-only p95 (`--version`) | ~40 ms | ≤ 100 ms |
| Cold CLI wall time | ~500–650 ms | n/a |
| Warm CLI max RSS p50 | ~16 MB | n/a |
| Service spawn → first snapshot (`app run`) | ~1.4–1.6 s (includes fixed 1 s pre-snapshot sleep) | ≤ 500 ms after NATIVE-M2 handshake/event protocol |
| Idle service CPU / RSS | ~0 % / ~16–18 MB | ≤ 0.2 % / ≤ 40 MB |
| Proxy non-streaming additive overhead | ~0.3 ms/request (loopback stub) | ≤ 10 ms p95 |
| Streaming first-byte direct vs proxied | ~0.09 vs ~0.11 ms (stub upstream) | tracked |
| Stripped Go binary | ~10.7 MB (gzip -6 ≈ 4.4 MB) | n/a |

Prior reference: warm Go CLI ≈ 2× faster than Node CLI with <½ max RSS on the
same fixture root (earlier sample; superseded in reporting by the script above).

---

## NATIVE-M0 — Freeze target contract and baseline

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Cutover tracker inventory | this file | `NATIVE_APP_CUTOVER.md` | review | review | all | **complete** | Every milestone below has rows; keep current after every slice |
| Architecture ADR (full app, one Go engine, bounded stdio, Node-free native artifacts, npm as distribution only) | AGENTS.md invariants | `adr/0020-node-free-native-app-distribution.md`, `adr/README.md` index | review against accepted ADRs 0004/0006/0009/0011/0015/0018 | review | all | **complete** | ADR 0020 supersedes ADR 0019 and removes the private-Node and retained-Ink exceptions |
| Ownership-inversion record | src/tray/*, go/cmd/anypick/main.go, tauri main.rs | table above | source inspection | n/a | all | **complete** | Three inversions recorded with exact paths |
| Reproducible perf baseline script | go/internal/proxy/baseline_bench_test.go | `scripts/perf-baseline.mjs`, `go/internal/proxy/baseline_bench_test.go` (`newUpstreamProbe`/`newTestServer` widened to `testing.TB`) | `node scripts/perf-baseline.mjs --runs 20` | report generated from built artifact + spawned service | macOS done; Linux/Windows runs pending CI job (NATIVE-M8) | **complete** | Proxy/streaming benches live in-package so stubs stay hermetic |
| Reference measurement report | scripts/perf-baseline.mjs output | table above; `dist/perf-baseline.md` (generated, not committed) | rerun script | same | all | **complete** | Machine metadata included; numbers are references, not promises |
| Platform matrix definition | product decisions §10/§11 | section above | review | deferred to install smokes per milestone | all | **complete** | Linux min baseline stated; arm64 Win/Linux excluded until tested |

Acceptance gate status: met (tracker rows exist for every milestone; ADR
consistent with accepted records; baselines reproducible with metadata; no
production behavior changed).

## NATIVE-M1 — Make the Go engine cutover-safe

Dependencies: NATIVE-M0. Detail contract: `go/PARITY_PLAN.md`; row-level state in
`go/PARITY.md` (GO-PARITY-M0…GO-PARITY-M10). The rows below mirror the
cutover-critical subset — do not mark any of these complete while the
corresponding GO-PARITY row is not complete. Verification that structurally
requires a later milestone's harness is split out to that milestone's rows so
NATIVE-M1 has no dependency cycle with NATIVE-M2/M5/M8.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Backup export/import preserves accounts/inspectionId/entryIds/overwriteConfirmed; no silent selection expansion (service layer) | src/core/account-backup.ts, account-restore.ts, tests | go/internal/transfer/backup.go, internal/desktop/{handlers,inspections}.go | `go test ./internal/transfer -run TestParity` + `npx vitest run tests/parity-backup.test.ts` (cross-runtime fixtures; selection non-expansion asserted both directions) | live producer/consumer exchange `pnpm go:parity` (passes locally) | go (all) | **partial** | Behavior and cross-runtime format are drift-tested, and `.github/workflows/ci.yml` now has a dedicated live-parity job. Keep partial until that new job is observed green on the committed tree and the corresponding GO-PARITY-M0 row closes. Desktop-protocol all-command e2e re-verification remains split to NATIVE-M2's cross-process harness row |
| link/unlink/reset match TS project/global binding semantics | src/core/binding-service.ts, adr/0007 | go/cmd/anypick/bindings.go over internal/bindings service | cmd e2e TestLinkUnlinkAreProjectScoped; TestLinkWithoutWithPreservesGlobalSpec (deep spec equality + provenance) | alternate Go/TS over one fixture root via parity drift gate | go | **complete** | Project-scoped by default (TS semantics); `link` without --with reapplies the entire global spec; `reset` clears client config AND global binding under client lock |
| proxy pool disable disables (no enable-path call); strategy/mode flags explicit | src/core/proxy-service.ts pool paths | go/cmd/anypick/pool.go, internal/pools | cmd e2e TestPoolDisableActuallyDisables | pool lifecycle e2e in smoke.sh | go | **complete** | `disable` calls DisableContext under provider scope and stops a detached child; `--strategy/--mode` validated/rejected explicitly. Real pool transport/failover server is the separate missing row below |
| Import/restore mutations under sorted provider-scoped coordinator locks | adr/0009, 0011; src/core/service.ts | internal/accounts, internal/transfer (locks inside ImportEnvelope / RestoreSelected) | lock-scope tests (-race); TestBackupExportInspectRestoreAtomic | concurrent import vs switch race coverage under -race suite | go | **complete** | Coordination no longer depends on callers: ImportEnvelope takes the provider scope internally, RestoreSelected takes sorted provider scopes internally |
| Pool activation via `pool:<provider>` is real transport | src/core/activation-executor.ts, src/core/proxy-service.ts pool paths | internal/activation executor account-pool branch; go/internal/proxy/pool_server.go (credential ring, bearer auth, /health instance echo) + service_pool.go (startPoolProxy/stopPoolProxy port: token reuse, primary config w/o authDirs, provider-scope locks, lease keyed (provider,"")) | executor tests TestExecutePoolActivationFillsManagedProxy / TestExecutePoolRollbackStopsPool / TestStartManagedProxyBranchesUnchanged; proxy failover suite TestPool* (-race); quota-guard wiring covered by TestPoolFailoverOnQuotaClassFailures + guard-file secret-free test | cmd e2e TestPoolActivationEndToEnd (fixture accounts → pool enable → use claude --with pool:<p> through the shared port → zero leaked _serve-pool children); smoke.sh activate-through-pool section | go | **complete** | Strategy persisted but rotation intentionally NOT implemented — TS parity: always ordered-failover with active promotion via Quota Guard; failover on 401/402/insufficient-balance/credential-quota PLUS provider-declared quotaErrorPatterns threaded from CompatibleDefinition through ResolveUpstream into the classifier (TS backend.ts parity). Review round fixed: verbatim passthrough bodies doubled by capture+replay; token rotation; deterministic planned/served port; verified CLI stop/disable. Current follow-up fixes preserve all pool fields on member toggle, require an explicit valid state, restart only a verified running shared-pool lease after a CLI toggle, and implement/test `restartRunningPools` for Quota Guard policy changes. Still open: live e2e coverage of member-toggle restart and the `quotaGuardEvents` observability surface |
| Proxy Hub end-to-end (route create/auth/model routing/failover/wrong token/cleanup) | src/core/proxy-hub-*.ts | internal/hub, internal/activation/executor.go, internal/proxy/{pool_server,service_pool}.go (HubPoolRing + PoolServer.ServeAuthorized for `pool:<provider>` sources) | hub_rollback_test.go (exact-row restore, hub scope held, detach-when-absent); route_lifecycle_test.go TestRouteAttachGrantRotateRevoke + TestHubPoolSourceFailsOverQuotaClassOnly / PassesNonQuotaThroughOnce / FailsClosedWhenPoolDisabled (-race); cmd e2e TestHubRouteLifecycleEndToEnd | e2e route attach/rotate/revoke, per-model routing through the detached child (`#N` pin → 502 names the resolved account), wrong token 401, detach under hub scope → 401, verified stop leaves zero leaked children and clean rows; pool failover proven at the live-server level (quota-class advances with guard cooldown; other statuses verbatim once) | go | **complete** | Activation holds `hub/<name>` for the whole critical section; AttachRoute overwrite compensates via byte-exact row snapshot restore; spawned child stop is instance-verified. Token-scoped attach/detach and failover now proven: rotate = re-attach (old 401/new 200), DetachRouteContext revokes immediately; route_lifecycle_test also asserts egress credentials on BOTH pool and non-pool branches (upstream sees the member's own credential, never the hub route token). Pool-backed sources reuse the existing Go refs `pool:<provider>` serialization as the Source.Account value (TS persists `{kind:'account-pool'}` ref objects; the Go store now adapts that shape on read and writes the TS decoder's exact key set back, see store.go decode/marshalTSConfig + store_parity_test.go cross-runtime fixtures in testdata/parity/hub-config.json), served through the standalone-pool credential ring with quota-class-only failover; no cross-source failover either side. Codex live-route sync after Hub changes remains a separate GO-PARITY-M6 row |
| Complete CLI tree/global flags/JSON/version injection/help golden/completion | src/cli/*, tests/golden.test.ts | go/cmd/anypick, go/internal/cli, go/internal/buildinfo | smoke.sh version checks + cmd e2e global-flag regressions + help_parity_test.go (cli-tree.json golden gate, JSON stdout purity matrix, verbosity regressions, __complete) | CLI smoke.sh (incl. completion/__complete + verbosity section) | all | **partial → CLI parity parts 1 + 2A closed** | Structural command-tree/dispatch golden, one-value JSON purity matrix, verbosity consumers, dynamic completion, version injection, dry-run breadth for 22 mutation paths, non-TTY confirmation gates, and plugin CRUD/trust/loaded/failure reporting are covered. Still open: `account refresh --dry-run` and full per-command human/JSON default parity (`list`/`account list`; `current` semantics/shape) |
| Detached-child ownership = structured ADR-0003 records (hub/pool/supervisor), unix | adr/0003; src/utils/process.ts | go/internal/ownership; cmd/anypick/detached.go + internal/app SpawnHubChild + cmd/anypick/{hubcmd,lifecycle,pool}.go rewired | internal/ownership tests (legacy fail-closed, argv identity, health veto) | TestHubActivationAdoptsAndStopsChild through echo-gated stop; PID reuse cannot cause kills | macOS/Linux (unix) | **complete** | Numeric-PID adoption removed on every path; hub stop gated on /health instance echo, fails closed with HUB_OWNERSHIP_UNVERIFIED. Identity no longer depends on `/bin/ps` (sysctl on darwin, /proc on Linux) and records carry a start-time stamp against PID reuse. Windows equivalents split out to the NATIVE-M5 platform-primitives row (no M1→M5 cycle) |
| Desktop protocol frame contract parity (limits, canonical validation, strict decode) + route-action semantics | src/tray/protocol.ts, src/tray/supervisor-actions.ts | go/internal/desktop/{protocol_validate.go, protocol_strict_test.go}, snapshot.go routeActivator → PlanActivation/Execute | boundary tests (64×16 entryIds, export cap 64, exact key sets); route_actions_test.go (gateway chip writes binding, cross-provider chip binds the client not the source provider, current-route re-invoke re-applies) | desktop protocol fixtures (NATIVE-M2 harness) | all | **complete** | Review P1-3 fixed limits (entryIds 64×16, export 64, setting-quota-guard rename); review round 3 fixed route actions bypassing the planner (no-op successes + wrong-scope account mutation on cross-provider chips) with regression coverage. Command/snapshot *completeness* lives in NATIVE-M2 rows and GO-PARITY-M9 by design |
| Settings/login/cancellation/snapshot/quota-guard parity | src/core/config*, tray-settings, quota-guard-policy, providers login | internal/config, internal/desktop/server.go settings ops, internal/accounts login checkpoint/cancel, internal/upstream quota guard | settings_test.go; browserlogin escalation tests; TestLoginCancelInCommitWindowRollsBack; upstream -race suite | desktop login cancellation e2e (TestDesktopAccountLogin) | go | **partial** | Done: four setting mutations (+ typed launch-at-login host effect, ADR 0020 §7), persisted preferences, cancellable login/commit rollback, gateway refresh using stored secrets without echoing them, quota-guard policy + Retry-After semantics, restart of verified running pools after a Quota Guard toggle, and structured degraded conditions consumed by the native Overview. Open: snapshot sections still empty (activity, hubSources/hubConflicts, clientModelConfigs), quota-guard event surfacing, and Codex live-route sync after Hub changes (GO-PARITY-M6/M9) |
| Executable ESM plugin host removed; registry retained as an inert seam | adr/0012/0014/0015; superseded ADR 0019; ADR 0020 §5 | internal/app/plugin_host.go replaces internal/pluginhost + plugin_runtime.go + plugin_supervisor.go | plugins_test.go (every candidate degrades, tamper still refused, diagnostics inert); cmd/anypick/plugins_test.go; scripts/npm-launcher-test.mjs "must not ship" assertions | n/a — nothing executes | all | **complete** | Deleted 2026-08-29 in one Go+JS pass (`09ab7e1`): `go/internal/pluginhost` (2.117 LOC + 838 test), `app/plugin_runtime.go` (1.720), `app/plugin_supervisor.go` (1.016), `app/plugin_recovery_test.go` (1.407), `app/plugin_proxy_test.go` (750), `scripts/plugin-host.mjs` (729 + 399 test) and `plugins/popular-providers/` (498). What survives deliberately: `internal/plugins` (registry: manifests, whole-package digests, enable/disable, add/remove) and `cmd/anypick/plugins.go`, so `anypick plugin …` still works; `proxy.PluginLifecycle` and `internal/proxy/service_plugin.go`, which are now unimplemented but are the contract a Go-native declarative executor will satisfy. Every candidate degrades to a `LoadFailure` naming `ErrPluginHostRemoved` with `Untrusted == false`, so `doctor` and the desktop protocol stay authoritative instead of reporting a healthy empty plugin set; tampering is still caught in `Inspect` first. `healthView` reports `inactive` (not `degraded`) and `Identity()` returns the zero value, so `internal/proxy` fails closed. ADR 0012/0015 are partly inert until a Go declarative executor lands |

ADR 0020 removes arbitrary executable JavaScript-plugin parity from the native
release scope; the host was deleted rather than kept as provenance (decision
2026-08-29). Its catalog-policy and plugin-pool gaps are cancelled as
native-cutover work. The 7 vendors that only existed in the deleted ESM pack
(`ark`, `tokenhub`, `groq-cloud`, `together-ai`, `fireworks`, `cerebras`,
`nvidia-nim`) were compiled into `provider.BuiltinDefinitions()` first
(`a3646c3`) so the deletion is not a silent regression.

Preserving those ids verbatim was a mistake that is now corrected: the pack
registered vendors the built-in table already had, so the port shipped
duplicate rows the user could not tell apart. `together-ai` was **dropped**
(same name "Together AI" as the built-in `together`, whose `api.together.xyz`
is the documented endpoint); `groq-cloud` was **renamed** to "GroqCloud (native
/responses)" because it shares `api.groq.com` with `groq` but enables
`NativeResponses`, which `groq` does not. `ark` stays — same host as `doubao`
but a different product. Net: 24 → 23 built-ins.
`TestEveryBuiltinCompatibleDefinitionIsValid` now fails on duplicate display
names as well as duplicate ids, so this cannot recur.

Acceptance gate: not met. Gate = every remaining in-scope GO-PARITY row complete +
cross-runtime fixture alternation + `-race` green + injected version + refuses
newer schema/protocol.

## NATIVE-M2 — Stabilize the app service contract

Dependencies: NATIVE-M1.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Hidden `desktop-service --stdio` on normal composition root | src/tray/supervisor.ts | go/cmd/anypick, internal/desktop | TestDesktopServiceRequiresExplicitStdio | TestDesktopServiceCrossProcessHandshake | all | **complete** | Hidden entrypoint uses the normal app composition root and never launches a UI; legacy `app` remains the compatibility alias |
| Startup handshake (protocol/app version, capabilities, schema version, non-secret instance id) | ADR 0020 §6 | internal/desktop/protocol_service.go; macOS AppServiceClient; Tauri protocol.rs | Go handshake/reject/canonical tests + Xcode client tests | real Go cross-process handshake; macOS bundled-app ready smoke | all | **partial** | Protocol v2, capability/version gating, non-secret instance id, and removal of the service-path 1 s sleep are proven. Still missing: installed mismatched-bundle degraded-screen/zero-mutation e2e and target-OS Tauri smoke |
| Exactly-once command results incl. cancellation/shutdown races | src/tray/supervisor-mutations.ts | internal/desktop | invoke/login cancellation + host-effect duplicate suppression tests | kill/restart harness | all | **partial** | Exactly-one terminal result is covered for invoke and cancellable login; shutdown waits for in-flight work and calls the composition-owned service sweep. Generic kill/restart race coverage is still missing |
| Snapshot model: initial + post-mutation events + bounded usage refresh + explicit refresh | src/tray/snapshot*.ts | internal/desktop/server.go; macOS Store.swift | protocol service frame-sequence tests | macOS bundled-app initial-snapshot smoke | all | **partial** | Initial snapshot, post-mutation/explicit-refresh snapshot events, and 30 s bounded usage events are implemented without full-snapshot polling. All-mutation real-pipe sequences remain open |
| Typed host-effect request/result messages | ADR 0020 §7 | internal/desktop; macOS GoServiceProcess + SMAppService; Tauri typed unavailable response | Go fake-host round trip + Swift decoder test | Swift executes login-item effect requested by Go | macOS/Win/Linux | **partial** | Typed request/result and host-truth reflection are implemented; installed SMAppService toggle/approval e2e is not |
| Bounded canonical frames; malformed input deterministic | src/tray/protocol.ts limits | internal/desktop/protocol_service.go; macOS AppServiceClient; Tauri protocol.rs | strict/canonical/bound tests | malformed stream doesn't kill service | all | **partial** | Go, Swift, and locked Rust gates pass; Tauri reassembles raw stdout chunks with a bounded CRLF-aware buffer and rejects invalid UTF-8/oversized/unterminated frames. Keep partial until target-OS malformed-stream app smoke passes |
| Fake-service harness for UI tests | tests/macos-design-ui.test.ts pattern | go/internal/desktop/fake or scripts | used by Swift/Tauri test targets | UI tests run against fake | macOS/Win/Linux | missing | Shared with M3/M5 |
| Real cross-process harness (temp HOME/ANYPICK_HOME, all commands) | go/scripts/smoke.sh style | go/cmd/anypick/desktop_service_e2e_test.go | `go test` spawning built binary | exercises every command end-to-end | all | **partial** | Temp-root built-binary handshake/initial snapshot/quit is covered. The all-command matrix, including backup selection over the real pipe and crash/restart, is still missing |

Acceptance gate: in progress; protocol v2/service ownership is real, but the
all-command, mismatch/degraded, race, and target-OS app harnesses are incomplete.

## NATIVE-M3 — Make `src/macos` a first-class application

Dependencies: NATIVE-M2.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Preserve System Settings IA/window/Settings scene/MenuBarExtra | src/macos/README.md | src/macos shared Swift tree | Xcode build/tests + `pnpm macos:design:check` | visual audit | macOS | **partial** | First-class app builds the existing IA from the shared source manifest; formal visual/lifecycle audit remains |
| Real Xcode project/schemes, Info.plist, entitlements, asset catalog, unit/UI test targets, archive config | src/macos/README.md | AnyPick.xcodeproj, shared scheme, Info.plist, entitlements, Assets.xcassets, AnyPickTests | local `xcodebuild ... test` passes 5 tests | archive produces .app with helper | macOS | **partial** | App + unit-test target and archive action exist; no XCUITest target and no archive/install result yet |
| Bundle Go helper into Contents/Helpers deterministically | — | Xcode Bundle Go Helper phase + scripts/bundle-go-helper.sh | helper existence/executable XCTest; local universal arch check | bundled app-service smoke passes locally | macOS | **partial** | Debug app launches bundle-relative helper and negotiates a snapshot; Release app/helper are both x86_64+arm64. Plugin-host/ESM plugin resources are no longer copied (ADR 0020). Archive/sign/install gate remains |
| App-owned `GoServiceProcess` actor (bundle resolution, pipe ownership, handshake, async IO, bounded restart, graceful stop, degraded state) | ADR 0020; supervisor.ts reference | src/macos/GoServiceProcess.swift + AppServiceClient.swift | Xcode protocol tests | bundled app-service smoke | macOS | **partial** | App owns `desktop-service --stdio`, dedicated pipes, restart bounds, quit, and degraded state. Crash-recovery smoke is still missing |
| Refactor TrayStore → app/desktop protocol store; separate transport/state/features | src/macos TrayStore | AppServiceClient + GoServiceProcess separated from Store/ProductionModel | Xcode protocol test + app smoke | production flows still pass | macOS | **partial** | Transport/process ownership is separated; broader store/feature decomposition and fixture parity are unfinished |
| Remove runtime Swift compilation from production paths | src/tray/supervisor-native.ts | Xcode path is toolchain-independent at runtime; legacy Node path not retired | grep gate: no swiftc/xcrun in prod launch | clean-account launch without Xcode | macOS | **partial** | New app path never invokes swiftc, but `supervisor-native.ts` still does; cutover cleanup is required |
| Launch at login via SMAppService; quiet login start; Settings reflects denied auth | Apple docs (SMAppService) | macOS typed host effect in GoServiceProcess | Go fake-host round trip | login-launch smoke | macOS | **partial** | SMAppService register/unregister and requiresApproval guidance exist; installed login-launch smoke is absent |
| Deterministic lifecycle tests (reopen/Dock/close-window/Settings/menu-bar/crash/quit) | src/macos/README.md behavior section | AppDelegate + GoServiceProcess; composition-owned `ShutdownOwned` sweep | shutdown callback regression + Xcode tests | installed-app smoke list in prompt | macOS | **partial** | Explicit quit gracefully stops the service, verified Hub children, pools, then every account proxy regardless enabled toggle. Pool-first order is required because the TS-compatible schema stores pool runtime state against its primary account. Reopen/close/menu/settings/crash and installed lifecycle coverage remain |
| Signing/package prep (universal, nested Go-helper signing, Hardened Runtime, minimal entitlements) | Apple notarization/hardened-runtime docs | release scripts | codesign/spctl/staple checks | ad-hoc artifact passes structure checks | macOS | missing | Production release blocked until Developer ID + notarization pass; no JIT or private runtime entitlement is allowed |

Acceptance gate: in progress; local Xcode/helper/app-owned smoke is green, but
archive/install, UI/lifecycle, crash recovery, signing, and legacy-path removal remain.

## NATIVE-M4 — Establish and enforce performance budgets

Dependencies: NATIVE-M1–NATIVE-M3.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Repeatable Go service + app-start benchmarks | scripts/perf-baseline.mjs | extend script + go bench targets | script run | CI/performance job artifacts | all | partial | Baseline exists; app-launch (Xcode-built) and 100-account decode pending |
| Fast help/version paths (no DB init/maintenance) | go/cmd/anypick main.go early-exit | already partially present (`--version` short-circuits) | assert no DB file created on --version/--help | — | all | partial | Verify `help` also avoids bootstrap |
| Budget enforcement (initial budgets in prompt §M4) | dist/perf-baseline.md schema | budget check section of script | script exit status on regression | retained artifacts + machine metadata | all | partial | Script prints budget table; CI enforcement lands with M8 jobs |
| Remove measured-only hotspots (polling, repeated reads) | measurements | targeted fixes | before/after bench | no correctness/cancel/security regressions | all | missing | Only where measurement proves impact |

Acceptance gate: not met (needs NATIVE-M1–NATIVE-M3 to land first).

## NATIVE-M5 — Turn Tauri into the Windows/Linux app

Dependencies: NATIVE-M2, NATIVE-M4 (NATIVE-M3 is the lifecycle reference).

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Product identity `AnyPick` + stable ids; resizable main window + taskbar entry | prompt §M5 | tauri.conf.json, Rust | config checks | installed launch | Win/Linux | **partial** | Product/identifier/window/taskbar config is present; installed target-OS launch is not |
| Go sidecar via `externalBin`, launched from Rust; React never spawns shells | tauri sidecar docs | src-tauri build.rs/config + sidecar.rs/protocol.rs | Rust protocol/bridge tests | UI drives real service | Win/Linux | **partial** | Production Rust owns sidecar launch, v2 negotiation, bounded raw-chunk framing, three-restart budget with 30 s stability window, graceful quit, and command forwarding; stdio is fixture-only. `cargo test --locked` passes 16 tests and `cargo check` is clean; packaged target-OS real-service UI smoke still gates completion |
| Port platform primitives (Windows locking/job objects/PID checks/credential paths/ACLs; Linux process/XDG/credential stores) | TS counterparts in src/**; unix reference: go/internal/ownership | Windows build-tag files for lock, ownership/process identity, Job Objects, proxy/CLI/browser children; existing XDG paths | local windows/amd64 whole-tree compile passes; windows-latest job wired | installed smoke | Win/Linux | **partial** | Real Windows execution is wired but not observed green here; credential-store/ACL breadth, installed smoke, and explicit IPv4+IPv6 listener coverage remain incomplete. Cross-compile alone is not support |
| Reuse shared protocol fixtures + fake/real harnesses | NATIVE-M2 outputs | fixtures dir consumed by Rust tests | cargo test fixtures | same | Win/Linux | missing | One fixture set for Go/Swift/Tauri |
| Windows NSIS (signed) ; Linux AppImage/deb/rpm on oldest supported base | tauri distribute docs | bundler configs | artifact inspection | installed smoke w/ restricted PATH | Win/Linux | missing | Unsigned builds never presented as release-ready |
| Win/Linux CI building on target OS | .github/workflows | Windows Go whole-tree job + existing Linux Go/Tauri jobs | job green | installed-app tests | Win/Linux | **partial** | CI wiring exists, but Windows Tauri build/installer and installed-app tests do not; no green run on this uncommitted tree is claimed |

Acceptance gate: in progress; sidecar ownership and Windows Go primitives are
implemented, while target installers and installed-app evidence remain missing.

## NATIVE-M6 — Native release artifacts contain no Node runtime

Dependencies: NATIVE-M1 plus NATIVE-M3 for macOS; NATIVE-M5 is required only
for the later Windows/Linux artifact gates. macOS may close this milestone for
its own artifact before those shells are release-ready (ADR 0020 §9).

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Remove user-toolchain invocation from every production-native path | ADR 0020 §3 | launchers/build outputs | artifact scan fails on forbidden executables, scripts, dev absolute paths, or PATH expectations | restricted-PATH clean install | all | **partial** | JS and production shell scanners share `scripts/node-free-artifact-fixtures.mjs` and must agree. Cross-tests fail external/dangling symlinks, Node shebang, `/Users` after byte 4096, forbidden names, and stale plugin-host. Xcode/CI call the full shell contract. Clean-account restricted-PATH install remains |
| Remove JavaScript plugin-host assets and activation from native composition | ADR 0020 §§3/5 | macOS bundle phase, Tauri resources/sidecar, Go desktop composition | build-tree and environment scan; built-ins/declarative compatible providers remain green | installed apps exercise first-party providers without Node present | all | **complete** | Xcode and Tauri stopped copying plugin-host.mjs/ESM packs and injecting ANYPICK_PLUGIN_HOST_* earlier; the host itself is now gone (decision 2026-08-29, `09ab7e1`). Cut in one Go+JS pass because `tests/plugins.test.ts` booted `plugins/popular-providers`: `go/internal/pluginhost` (2.117 LOC + 838 test), `app/plugin_runtime.go` (1.720), `app/plugin_supervisor.go` (1.016), `app/plugin_recovery_test.go` (1.407), `app/plugin_proxy_test.go` (750), `scripts/plugin-host.mjs` (729 + 399 test), `plugins/popular-providers/` (498), the `ANYPICK_PLUGIN_HOST_*`/`ANYPICK_BUNDLED_PLUGINS` injection in `bin/anypick.js` and `scripts/dev-go.mjs`, and the `package.json` `files` entries. `scripts/npm-launcher-test.mjs` now asserts the packed tarball ships **no** `package/plugins/` or `package/scripts/` and that the child receives `injectedPluginEnv: []`. `internal/plugins` stays as the inert registry — see the NATIVE-M1 row for the seam contract. Installed first-party e2e without Node remains open |
| First-party and declarative extension coverage is Go-owned | ADR 0015; ADR 0020 §5 | internal/app providers/clients/catalog + declarative compatible definitions | composition and provider suites without plugin-host env | clean install covers account, activation, proxy, pool, Hub and backup flows | all | **partial → no executable-plugin dependence remains** | Product dependence on executable ESM plugins is gone: the 7 vendors that lived only in the deleted pack are compiled into `provider.BuiltinDefinitions()` (23 built-ins after dropping the duplicate `together-ai`, `a3646c3`), and `TestEveryBuiltinCompatibleDefinitionIsValid` validates every row (unique id, **unique display name**, `NewCompatible` accepts it, HTTPS-only endpoint profiles, non-empty credential labels). Still open: a Go-native executor for third-party declarative packages (`internal/plugins` registers but cannot activate them), and clean-install e2e for account/activation/proxy/pool/Hub/backup |
| Node-free artifact attestation and clean-install smoke | ADR 0020 §8 | release inspection scripts + per-OS installed tests | recursive contents/import/dependency scan | launch and core workflows on a clean account with restricted PATH | all | **partial** | Production `.app` gate now matches the JS scanner (names, plugin-host, shebang, full-file path leak ≤512 KiB, symlink escape/dangling). Malicious shared fixtures fail both scanners. Notarized install + restricted-PATH clean-account smoke remain |

Acceptance gate: not met. Every native artifact must pass the recursive scan and
installed clean-account smoke; arbitrary ESM plugin compatibility is explicitly
not part of this gate.

## NATIVE-M7 — Keep npm as a thin distribution adapter

Dependencies: NATIVE-M1, NATIVE-M2, native release binaries.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| npm launcher selects the platform Go binary; signals/stdio/exit codes/args pass through; single JSON stdout stream | ADR 0020 §4 | bin/anypick.js | launcher fixture tests | `npm exec` matches native CLI output/exit codes | npm | **partial** | Target detection and passthrough exist; the plugin-host environment injection and the `scripts/plugin-host.mjs` / `plugins` package entries are gone (`09ab7e1`), so the native branch spawns the Go binary with no injected environment at all. Still open: the `isTuiInvocation` branch (blocked on the Go TUI port, NATIVE-M7 row 3) and real per-platform npm smokes |
| Platform optionalDependency packages (@anypick/cli-*) with os/cpu/libc metadata; deterministic error when omitted | npmjs package.json docs | npm/cli-* manifests + workspace optionalDependencies + scripts/build-npm-binaries.mjs | manifest/launcher/staging/pack contract tests | install with omitted optional deps → clear error | npm | **partial** | Five manifests and a deterministic CGO-free Go staging script exist; it injects one version, validates Mach-O/ELF/PE target architecture/mode, prints SHA-256, and local dry-run packs contain exactly their binary. Publishing, target-runner installs/signing, and the `fs-ext` native-build requirement on the TS TUI path remain open |
| Port the interactive TUI to Go | ADR 0020 §4/§5 | go/internal/tui (bubbletea + lipgloss), wired in `go/cmd/anypick/main.go` case `"tui"` | 19 unit tests over the model and the pure display layer; pty smoke drives real keystrokes and asserts a clean exit | `anypick tui` in a real terminal lists providers, opens accounts, switches, starts/stops proxies | all | **partial → v1 landed** | **Decision 2026-08-29: the TUI is ported to Go, not retired.** `go/internal/tui` now covers the core loop: provider home (live-login relation, saved count, proxy state) → account list → detail, with use/switch, save-live, proxy start/stop, delete-behind-confirmation, reload and help. It calls the services directly (`a.Accounts`, `a.Proxy`, `a.Store`, `a.Providers`) because the CLI handlers live in `package main` and cannot be reused. `deriveLiveRelation`, `formatProxyLabel`, `identityDisplayText` and `formatRelativeTime` are ported verbatim from `src/tui/model/identity.ts` so both trees agree while both exist. Invariant #2 is enforced structurally: `StartResult.Token` is discarded, only the endpoint is shown. Invariant #6: `Run` refuses to start without a TTY on stdin and exits 2 with an actionable message. Deferred (deliberately, as the non-core surface): gateway CRUD, Hub routing, app bindings and the model-map editors. Remaining work is *deletion*, not porting: remove `src/tui` and the `isTuiInvocation` branch in `bin/anypick.js` |
| Trusted publishing/provenance kept | CONTRIBUTING.md release setup | release workflow | provenance on publish | publish dry-run | npm | **partial** | Workflow exists for TS package; extend for platform pkgs |

Acceptance gate: not met. Launcher/manifests exist and the launcher no longer
injects any plugin-host environment. The Go TUI now exists, so the
`isTuiInvocation` branch in `bin/anypick.js` is only waiting on deleting
`src/tui`; no platform package contains a release binary, and no per-platform
install smoke has passed.

## NATIVE-M8 — Multi-platform release pipeline

Dependencies: NATIVE-M3, NATIVE-M5, NATIVE-M6, NATIVE-M7.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| One version/tag across package.json, Go ldflags, MARKETING_VERSION, Cargo/Tauri, platform pkgs, brew/winget manifests | release-plan.mjs model | extend planner | consistency checker | dry-run release | all | **partial** | Release planner now synchronizes root npm, five platform manifests, Cargo lock/manifest and Tauri config; the Go staging script injects that version. Xcode MARKETING_VERSION, signed artifact manifests and brew/winget templates remain pending |
| Per-OS build jobs: clean checkout, locked deps, tests, assemble, sign, inspect tree, smoke, SHA-256 + SBOM, immutable artifacts | prompt §M8 | CI now wires Linux Go vet/test/race, Windows Go tests, live TS↔Go parity, and macOS Xcode/helper/smoke/universal checks | focused gates pass locally where noted | final release job verifies digests before publishing | all | **partial** | Wiring covers important pre-release slices but not complete per-OS assembly, signing, installers, SHA-256/SBOM, immutable artifact comparison, or a final digest-verifying publish job. No remote-green result is claimed |
| macOS sign→notarize→staple chain (nested helpers first) | Apple docs | release scripts | codesign --verify --deep --strict, spctl, stapler | DMG installs on clean account | macOS | missing | Never skipped-and-called-pass |
| Windows Authenticode + installer verify; WinGet manifest generation (no submit w/o approval) | MS docs | release scripts | signtool verify | clean install/uninstall | Windows | missing | |
| Linux packages verified (contents, desktop entry, icons, perms, upgrade/uninstall) | packaging docs | release scripts | pkg inspectors | install/upgrade/remove on base distro | Linux | missing | |
| Homebrew tap templates/tests (Cask + Formula) — no homebrew submission w/o approval | Cask Cookbook | templates + tests | brew audit on artifacts | brew install/upgrade/uninstall | macOS | missing | Project tap first |

Acceptance gate: not met; verification jobs are wired, release artifact jobs are not.

## NATIVE-M9 — Cutover, cleanup, final audit

Dependencies: all prior.

| Work item | Authority | Implementation | Focused verification | End-to-end verification | Platforms | Status | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Go is the only product service/CLI engine; remove Node supervisor and runtime Swift compilation | ADR 0020 | deletion + bin changes | banned-string scan | clean-machine flows | all | missing | npm may retain only its minimal platform-binary launcher glue |
| Remove Node-only TUI/public-API/plugin product paths; retain TS only for build/test/docs/frontend where still useful | ADR 0020 §§3–5 | prune or semver-major migration | `pnpm check` + product artifact scan | docs build + npm/native parity smoke | all | missing | Required behavior moves to Go; source-repository tooling is not shipped runtime |
| Docs: app-first install, channels, rollback; remove tray-first language | README, docs/src | docs edits | docs build | links valid | docs | missing | CHANGELOG frozen; use Conventional Commits |
| Mandatory independent P0–P3 review of every changed/new file | prompt checklist | review pass | findings log | all P0/P1 fixed | all | missing | Includes untracked files enumeration |

Acceptance gate: not started.

---

## Session log (ADR 0020 rebaseline + review round 3)

- Detached-child ownership no longer shells out to `/bin/ps` (2026-08-29):
  `internal/ownership` verified identity by running `ps`, so any environment
  where that binary is blocked — sandboxed CI, hardened runtime, minimal
  container — made every verification fail closed and `Stop()` refuse to
  signal, leaking the child it existed to reap. Identity now comes from
  `sysctl kern.procargs2` / `kern.proc.pid` on darwin and `/proc` on Linux.
  Records also carry a start-time stamp, so a recycled pid running identical
  arguments no longer verifies (Windows already worked this way). `Spawn`
  waits for the child to exec before capturing identity: a forked-but-
  not-yet-exec'd process reports the parent's argv and executable.
  Regressions: `internal/ownership/stop_reaps_child_test.go` (2 tests).

- Leak assertions are scoped and honest (2026-08-29): the CLI e2e suite used a
  machine-wide `pgrep -f _serve-hub`, which matches children left by other
  tests, other packages, or earlier runs — one real leak reddened everything,
  and a blocked `pgrep` read as "nothing leaked". Leak checks now match only
  the running test's temp root, fail loudly when detection is unavailable,
  report surviving ownership records alongside pids, and every lifecycle test
  asserts at end-of-test as well.

- Untracked work committed (2026-08-29): the entire Go engine (36k LOC product
  + 27k LOC tests) had never been in git, along with the macOS Swift app, ADRs
  0015–0020, the cutover tracker, the npm platform manifests, the launcher, the
  build/gate scripts, and 176 modified TypeScript/docs/CI files. `.gitignore`
  now excludes compiled artifacts (`*.exe`, `*.profraw`, `go/anypick*`), npm
  staging output, and local scratch — `go/anypick.exe` (17 MB) would otherwise
  have been committed.

- Legacy `~/.hotplug` root migration removed (2026-08-29): product decision 3.
  `internal/app/legacy.go` is gone, along with its bootstrap branch in
  `Ready()`, its SQLite inspection path, and its staging/rollback routine.
  `processAlive` moved to `internal/app/process.go`.

- Minimal Go TUI landed (2026-08-29): product decision 1. `anypick tui` was a
  stub that printed "ships with the TypeScript build"; it is now a bubbletea
  program in `go/internal/tui`. Dependency pinned to **bubbletea v1.3.6**, not
  v1.3.10 — the newer release needs go >= 1.24 and CI pins Go 1.23, so
  `@latest` would have bumped the module directive behind the team's back.
  Verified by 19 unit tests plus a pty harness that sends real keystrokes
  (home→enter→esc→q and help→esc→q both exit 0). An earlier "TUI does not quit"
  reading was a bug in the harness, not the program: the pty master raises EIO
  when the child exits, and the script broke out of its loop without recording
  the exit code.

- Executable ESM plugin host removed (2026-08-29): product decision 2, scoped
  by a follow-up question to "delete the host, keep the registry" so `anypick
  plugin …` survives as an inert seam. `09ab7e1` cut the Go and JS sides in one
  pass (~8.500 LOC) because `tests/plugins.test.ts` booted the ESM pack;
  `a3646c3` then compiled that pack's 7 unique vendors into
  `provider.BuiltinDefinitions()` (17 → 24) so nothing regressed silently.
  `internal/app/plugin_host.go` is the whole replacement: candidates degrade to
  `ErrPluginHostRemoved` failures, diagnostics report `inactive`, and
  `Identity()` returns the zero value so `internal/proxy` fails closed.
  `internal/plugins`, `proxy.PluginLifecycle` and
  `internal/proxy/service_plugin.go` stay — they are the contract a Go-native
  declarative executor will satisfy, not dead code.

- Architecture rebaseline (2026-08-26): ADR 0020 supersedes ADR 0019. Native
  downloads are full applications over one bundled Go core and contain no Node
  runtime or plugin-host script. npm is only a thin distribution adapter to the
  same platform Go binary. Arbitrary ESM plugin parity and the retained Ink TUI
  are no longer native-release requirements. This session added the artifact
  gate (`check-node-free-artifact.mjs` / `check-node-free-app.sh`) and stopped
  copying plugin-host/ESM plugin packs into macOS and Tauri bundles.

- GO-PARITY-M7 host recovery/restart (2026-08-26): replacement generations
  land with owner-aware per-plugin reconcile (cross-plugin takeover degrades
  only the offender), a reader-atomic publication gate released before host
  cleanup, and local-frame-rejection classification.
  Deleted 2026-08-29 with the rest of the host (`09ab7e1`); the rows above
  record the design that was superseded. ADR 0020 cancelled further
  native-release investment in the private runtime.
- Normalized milestone names: cutover milestones are NATIVE-M0…NATIVE-M9;
  `go/PARITY.md` milestones are nested workstreams GO-PARITY-M0…GO-PARITY-M10
  and are referenced as dependencies, never as the master phase.
- Split dependency-cycle rows out of NATIVE-M1: desktop-protocol e2e of the
  backup/frame-contract rows → NATIVE-M2 cross-process harness row; Windows
  detached-child ownership → NATIVE-M5 platform-primitives row; live parity
  drift-gate CI wiring → NATIVE-M8 per-OS build-jobs row.
- Incorporated session evidence into tracker rows: link/unlink/reset,
  provider-scoped import/restore locks, pool disable, and unix detached-child
  ownership now complete with e2e evidence; settings/login/cancellation/
  snapshot row re-scoped to its precise remaining gaps.
- Fixed P1: desktop route actions bypassed the planner (gateway/current-route
  chips = false success; cross-provider chips = wrong-scope account mutation).
  All route handlers now run PlanActivation(ModePersistent)/Execute.
  Regressions: go/internal/desktop/route_actions_test.go (3 tests).
- Fixed P1: planner gated StepWriteNativeAuth behind direct transport,
  diverging from TS WriteNativeAuth for auth-writing account sources over
  managed proxies. Regression:
  TestPlannerManagedProxyAccountStillWritesNativeAuth.
- Gates on final tree: gofmt clean; go vet clean; `go test ./... -count=1`
  and `-race` green (32 pkgs); scripts/smoke.sh 13/13.
- Protocol/native audit (2026-08-24): focused Go desktop/proxy/cmd tests pass;
  whole Go tree cross-compiles for windows/amd64 (compile-only); Xcode unit
  tests pass 5/5; the built app negotiates its bundled helper successfully;
  Release app and helper both contain x86_64+arm64 slices. CI wiring is present
  but no remote-green result, signed archive, installer, or clean-machine smoke
  is inferred from those local checks.

## Blockers / open questions

None requiring user decision yet. Production signing/notarization credentials
and any external publication remain blocked on explicit user approval
(product decision 12).
