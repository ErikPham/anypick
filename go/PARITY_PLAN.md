# Go Feature Parity Plan

This document is the implementation contract for bringing `go/` to feature parity with the
TypeScript implementation, except for the terminal UI. It is intentionally executable: each
milestone defines its dependencies, deliverables, review points, and acceptance gates.

The TypeScript implementation remains the behavioral authority until every in-scope row in
`go/PARITY.md` is complete and the final validation suite passes. The existence of a Go package,
store, type, or isolated unit test does not by itself establish feature parity.

## Scope

In scope:

- The complete CLI surface and global CLI behavior, except `tui` and `--tui`.
- Account, provider, catalog, client, profile, config, and binding lifecycles.
- Persistent activation, project activation, and ephemeral client launches.
- Presets and source-resume history.
- Per-account proxies, multi-account pools, and Proxy Hub.
- Account import/export and multi-account backup/restore.
- Live usage, model discovery, model roles, and provider login flows.
- Plugin installation, trust, activation, failure reporting, and disposal.
- Doctor, update, completion, diagnostics, and structured events.
- Desktop tray supervision, the macOS app protocol, and the Tauri tray protocol.
- Database, JSON, backup, route-manifest, and stdio-protocol compatibility.
- Packaging, smoke coverage, documentation, and CI needed to ship the Go implementation safely.

Out of scope:

- The Ink terminal UI and every TUI screen, keybinding, component, and rendering path.
- The `tui` command and `--tui` global option in the Go command tree.
- Changing intentionally unsupported capability pairs. `transportFor` remains authoritative.
- Supporting nested presets; TypeScript intentionally rejects them too.
- Claiming Windows support. Current project policy and CI do not establish it.
- Switching the released `anypick` command to Go before the cutover gate in this plan passes.

For a bare Go invocation, opening the desktop tray is valid when tray is the configured surface.
When the configured/default surface is TUI, print concise help explaining that TUI belongs to the
TypeScript build and exit successfully. Do not silently add a Go TUI.

## Behavioral authorities

Read the relevant TypeScript implementation and its tests before porting a feature. Start with:

- Composition and lifecycle: `src/core/app.ts`
- CLI bootstrap and global options: `src/cli/bootstrap.ts`, `src/cli/commands.ts`
- Primary CLI workflows: `src/cli/primary.ts`
- Plugin commands: `src/cli/plugin-commands.ts`
- Account/provider contract: `src/types/account.ts`, `src/core/service.ts`
- Client contract and built-ins: `src/types/clients.ts`, `src/clients/`
- Activation: `src/core/activation-planner.ts`, `src/core/activation-executor.ts`
- Bindings and presets: `src/core/binding-service.ts`, `src/core/binding-store.ts`,
  `src/core/preset-store.ts`
- Account transfer and backups: `src/core/account-codec.ts`, `src/core/account-transfer.ts`,
  `src/core/account-backup.ts`, `src/core/account-restore.ts`
- Proxy lifecycle: `src/core/proxy-service.ts`, `src/core/proxy-lifecycle.ts`
- Pool behavior: `src/core/pool-store.ts` and the pool paths in `src/core/proxy-service.ts`
- Proxy Hub: `src/core/proxy-hub-store.ts`, `src/core/proxy-hub-service.ts`,
  `src/core/proxy-hub-server.ts`
- Provider proxy/protocol behavior: `src/providers/`
- Model discovery and policy: `src/core/model-discovery.ts`, `src/core/model-policy.ts`
- Doctor: `src/core/doctor*.ts`
- Plugins: `src/core/plugin-loader.ts`, `src/core/plugin-service.ts`, `src/types/plugins.ts`
- Desktop protocol: `src/tray/protocol.ts`, `src/tray/supervisor*.ts`, `src/tray/snapshot*.ts`
- Native macOS consumer: `src/macos/`
- User-visible behavior: `README.md` and `docs/src/content/docs/`

Do not use `docs/history/` as the current specification. It is provenance only.

## Non-negotiable invariants

Every milestone must preserve the repository invariants in the root `AGENTS.md`, especially:

1. Capability pairs come from the source adapter's `transportFor` behavior.
2. Proxy secrets never cross stdout, JSON, logs, diagnostics, or process arguments.
3. Activation is planned, journalled, then executed from `ActivationPlan.steps`.
4. `writeMeta` is the atomic commit point for snapshots.
5. Stores own SQL; services own persisted mutation locks.
6. Account mutations use provider scope, not per-account lock scope.
7. No prompt without a TTY; machine output remains machine-readable.
8. All proxy listeners bind loopback and authenticate every credentialed request.
9. The data directory remains `~/.anypick`, with `ANYPICK_HOME` as the override.
10. Provider-specific behavior stays on provider/client adapters, not in core, CLI, or desktop
    switches.

## Persistent parity tracker

The implementing agent must create and maintain `go/PARITY.md`. It is the durable source of truth
across sessions and context compaction.

For every feature, command, or desktop operation record:

| Field | Required content |
| --- | --- |
| Feature | Stable user-visible or protocol behavior |
| TypeScript authority | Source and test paths |
| Go implementation | Source path |
| Go verification | Focused and end-to-end test paths/commands |
| Status | `missing`, `partial`, `complete`, or `blocked` |
| Notes | Intentional differences or remaining risks |

A row may be marked `complete` only when the behavior is reachable through the Go CLI or desktop
protocol, persisted formats are compatible, focused tests pass, and the relevant end-to-end
acceptance test passes.

## Milestone 0: Freeze the parity contract

Deliverables:

- Create `go/PARITY.md` by inventorying the TypeScript public surfaces listed above.
- Classify every row from direct source inspection; do not infer completeness from package names.
- Create `go/testdata/parity/` fixtures for stable cross-runtime boundaries:
  - account envelopes and multi-account backups;
  - bindings, presets, profiles, pools, and Hub route manifests;
  - desktop protocol commands, results, snapshots, logs, and backup previews;
  - proxy request, response, error, and SSE translations;
  - structured errors and exit codes;
  - CLI help and command-tree output.
- Add drift tests so a TypeScript contract change requires an explicit corresponding Go decision.

Acceptance gate:

- Every in-scope feature has a tracker row and a TypeScript authority.
- Every known intentional difference is explicit and tested.
- No row is marked complete only because a store or type exists.

## Milestone 1: Composition root and bootstrap

Dependencies: Milestone 0.

Deliverables:

- Move application construction out of `go/cmd/anypick/main.go` into a cohesive `go/internal/app`
  composition package. Keep command handlers thin.
- Construct and expose the complete service graph: accounts, profiles, config, tray settings,
  bindings, presets, pools, Hub, journal, leases, runtime, proxy, model discovery, plugins, doctor,
  and structured events.
- Port the complete `createAppReady` lifecycle:
  - `.migrate.lock` ownership;
  - legacy root migration when safe;
  - filesystem-to-SQLite migration;
  - binding migration;
  - config bootstrap;
  - incomplete-operation recovery;
  - stale lease reaping;
  - degraded plugin failure events;
  - idempotent application close.
- Register built-ins, activate plugins, and only then seal provider/catalog/client registries.
- Keep the Go and TypeScript schema migrations ordered and mutually readable. Refuse newer schema
  versions before writing.

Acceptance gate:

- TypeScript and Go can alternately open and mutate the same fixture `ANYPICK_HOME`.
- Concurrent bootstrap attempts serialize correctly.
- Startup recovery and stale lease reaping have failure-injection tests.
- Closing twice is safe and disposes successful plugins in reverse activation order.

## Milestone 2: Provider, client, catalog, usage, and model parity

Dependencies: Milestone 1.

Deliverables:

- Extend Go provider capabilities with focused optional interfaces for:
  - short names and account sources;
  - source-aware detect, backup, and clear;
  - credential input kinds, labels, fields, and non-secret options;
  - browser login methods and cancellable login;
  - refresh auth;
  - live usage;
  - restore preflight and live-owner detection;
  - model defaults, aliases, role-friendly models, and live discovery;
  - provider-owned proxy configuration and lifecycle behavior.
- Reach behavioral parity for Codex, Claude, Gemini CLI, Antigravity, Grok, Kiro, and OpenCode.
- Preserve the Codex desktop-account mismatch behavior that suppresses misleading quota output.
- Port every built-in client adapter. Go currently has only Claude and Codex; add Gemini, Kiro, and
  OpenCode with their validate/apply/inspect/reset, installation probes, model roles, isolation
  manifests, managed paths, and managed environment keys.
- Add model cache and live discovery with the TypeScript fallback chain: fresh cache, live fetch,
  stale cache, static catalog.
- Apply outbound origin allowlists, timeouts, cancellation, and secret-safe errors to live model and
  usage requests.

Acceptance gate:

- Each provider has contract tests for every capability it declares.
- Each client has persistent apply/reset and isolated-runtime tests.
- Provider/client matrices match `transportFor`; registry membership never grants compatibility.
- Model and usage network failures degrade to the same safe result as TypeScript.

## Milestone 3: Account, profile, config, and backup services

Dependencies: Milestone 2.

Deliverables:

- Complete account workflows: detect, save-current, new browser login, explicit credential input,
  source selection, edit, refresh one/all, remove, and clear live login.
- Preserve identity collision handling and original account creation timestamps.
- Add service-owned after-switch native binding realignment.
- Add service-owned after-delete Hub source/owner and pool-member cleanup.
- Complete gateway/profile create, show, edit, test, refresh, remove, and client reset workflows.
- Port global config, tray preferences, quota guard settings, and launch-at-login settings.
- Port byte-compatible single-account import/export.
- Port multi-account backup export, inspect, selective restore, and explicit replacement approval.
- Preserve all import/backup limits, terminal-control validation, traversal protection,
  file/directory collision protection, owner-only permissions, staging, and atomic commits.
- Imported proxy configuration must always be disabled and must drop provider-specific options.

Acceptance gate:

- Go exports import in TypeScript and TypeScript exports import in Go.
- A rejected or failed import leaves DB rows, active account, prior snapshot, and live auth unchanged.
- Batch restore is atomic.
- Every persisted mutation is covered by a lock-scope test.

## Milestone 4: Bindings, presets, and runtime

Dependencies: Milestones 2 and 3.

Deliverables:

- Port the complete binding service instead of embedding behavior in CLI commands.
- Support persistent `use`, `use --current`, and `use --save`.
- Support ephemeral `run` with pass-through arguments after `--`.
- Support project `link` and `unlink` with global/project precedence.
- Support precise client reset and source-resume history.
- Complete preset list/show/remove, snapshot provenance, revision, last-used, and use-count behavior.
- Preserve intentional rejection of nested presets.
- Complete isolated-home preparation, environment overlay, child launch, signal forwarding, and
  cleanup on success, failure, cancellation, and termination.
- A dry run must not write files or DB rows and must not start/stop any process.
- Port Codex live-route publication, native restoration, and model-role synchronization.

Acceptance gate:

- Persistent, project, preset, gateway, pool, Hub, and ephemeral plans have end-to-end tests.
- Planner/executor divergence is covered by rollback and failure-injection tests.
- Interrupted ephemeral runs leave no temporary home, process, lease, or managed live config.

## Milestone 5: Per-account proxy protocol and lifecycle

Dependencies: Milestones 2 through 4.

Deliverables:

- Reach protocol parity for OpenAI chat completions, OpenAI Responses, Anthropic messages, and model
  listing.
- Reach streaming parity for SSE framing, termination, usage, tool calls, reasoning/thinking,
  schema payloads, supported image inputs, and upstream errors.
- Preserve context-budget behavior and safe degradation for intentionally unsupported inputs.
- Port provider-specific upstream authentication and endpoint rules.
- Port quota guard, retry classification, and credential-scoped failover behavior.
- Complete provider proxy options including OpenCode auth mode and Gemini OAuth source.
- Complete enable/config/disable/start/stop/status/restart/log and log-follow lifecycle.
- Realign clients after endpoint/port changes.
- Preserve PID, process-group, instance ownership, lease, log, and stale-process semantics.

Acceptance gate:

- Shared protocol fixtures produce behavior equivalent to TypeScript.
- Every listener is loopback-only.
- Every credentialed route rejects missing or incorrect instance credentials.
- Secrets never appear in public handles, stdout, JSON, diagnostics, logs, or process arguments.
- Lifecycle race and cancellation tests pass under `go test -race`.

## Milestone 6: Multi-account pools and Proxy Hub

Dependencies: Milestone 5.

Deliverables for pools:

- Add a service layer over the existing pool store; stores must not own coordination.
- Implement status, enable, disable, member on/off, start, stop, and restart.
- Implement provider-owned pool transport, failover, and round-robin where configured.
- Implement pool activation through `pool:<provider>`.
- Keep account names aligned with credential rings and quota decisions.

Deliverables for Proxy Hub:

- Wire the existing Hub store/server into application composition and activation.
- Implement status, enable, source toggle, explicit model owner, preview, start, and stop.
- Implement source catalog refresh, conflict reporting, and deterministic route compilation.
- Implement token-scoped route attach/detach, rollback, and restart recovery.
- Route account and pool upstreams through OpenAI/Anthropic surfaces as declared.
- Publish models through the correct route manifest and enforce route ownership.
- Synchronize Codex live configuration after Hub lifecycle changes.
- Forget sources/owners/routes when accounts are removed.

Acceptance gate:

- The current `Proxy Hub service is unavailable` and `pool lifecycle not yet ported` paths are
  replaced by real behavior.
- End-to-end tests create a route, authenticate it, route distinct models correctly, exercise
  failover, reject the wrong token, and clean up after account removal.
- Hub route secrets never enter snapshots or public status.

## Milestone 7: JavaScript plugin compatibility

Dependencies: Milestones 1, 2, and stable provider/client contracts.

Do not invent a Go-only plugin API. Existing plugins are ESM modules with executable provider and
client behavior. Exact backward compatibility requires a Node plugin-host sidecar.

Deliverables:

- Port plugin manifest parsing, plugin store, add/remove/enable/disable/trust, and package hashing.
- Match the TypeScript whole-package digest algorithm and its skipped-directory rules exactly.
- Verify the package digest before any plugin module is imported.
- Add a versioned, cancellable Node-host RPC protocol with bounded messages and explicit timeouts.
- Activate plugins before Go registries seal.
- Support all existing registration methods:
  - `registerProvider`
  - `registerClient`
  - `registerCatalogProvider`
  - `registerCompatibleProvider`
- Materialize inert compatible-provider definitions as native Go adapters.
- Represent arbitrary provider/client registrations with RPC-backed adapters implementing the same
  optional capability semantics as built-ins.
- Treat a broken, changed, or unavailable plugin as a degraded startup condition; keep built-ins
  usable and report failures through plugin list, doctor, and structured events.
- Dispose successfully activated plugins in reverse order.
- Keep plugin-host stdout protocol-only and sanitize stderr/logging.

Acceptance gate:

- The same fixture plugin registers all four extension types in TypeScript and Go.
- A changed helper file causes trust refusal before module import.
- A plugin that throws after partial registration leaves no partial registry state.
- Plugin-host cancellation, crash, timeout, and disposal have deterministic tests.

Node is an optional runtime dependency only when JavaScript plugins are enabled. Embedding a partial
JavaScript engine does not satisfy parity with Node ESM and is not an acceptable shortcut.

## Milestone 8: Complete CLI parity

Dependencies: Milestones 1 through 7.

Split command handling into cohesive files while keeping business logic in services. Avoid adding a
CLI framework unless it materially simplifies the complete command tree and preserves output
behavior.

Required command tree:

- `use`, `run`, `current`, `list`
- `add account`, `add gateway`
- `link`, `unlink`, `reset`
- `preset list`, `preset show`, `preset remove`
- `account list`, `account remove`, `account refresh`, `account export`, `account import`
- `gateway list`, `gateway show`, `gateway edit`, `gateway remove`, `gateway reset`
- `proxy status`, `proxy start`, `proxy stop`, `proxy enable`, `proxy config`, `proxy disable`,
  `proxy logs`
- `proxy pool status`, `proxy pool enable`, `proxy pool disable`, `proxy pool member`, pool
  start/stop behavior
- `proxy hub status`, `proxy hub enable`, `proxy hub source`, `proxy hub owner`,
  `proxy hub preview`, `proxy hub start`, `proxy hub stop`
- `plugin list`, `plugin add`, `plugin remove`, `plugin enable`, `plugin disable`, `plugin trust`
- `tray`, `start`, `status`, `stop`, and required hidden supervisor commands
- `update`, `completion`, `doctor`, `providers`, `clients`
- Required hidden dynamic-completion and proxy child commands

Required global behavior:

- `--json`, `--dry-run`, `--no-input`, `--yes`
- `--quiet`, `--verbose`, `--trace`, `--reveal`
- `--tray`; deliberately exclude `--tui`
- `--help`, `--version`
- Prompt only when a TTY is present.
- JSON stdout is exactly one parseable result stream; progress and diagnostics go to stderr.
- Secret-bearing inputs prefer environment variables or prompts and warn when process arguments
  expose them.
- Errors retain structured codes, mutation state, suggestions, and `ExitCode` behavior.
- Completion covers providers, accounts, gateways, clients, and shells supported by TypeScript.

Acceptance gate:

- CLI command-tree and help golden tests match TypeScript except documented TUI omissions.
- Every command has human, JSON, invalid-usage, not-found, no-TTY, and dry-run coverage where
  applicable.
- Machine-readable commands never print banners, spinners, warnings, or secrets to stdout.

## Milestone 9: Desktop tray and macOS protocol parity

Dependencies: Milestones 1 through 8.

Deliverables:

- Match `TRAY_PROTOCOL_VERSION`, size limits, canonical base64 validation, and malformed-input
  handling.
- Complete revision-bound invoke actions:
  - account switch;
  - proxy enable, disable, and restart;
  - Hub own-models, test, start, stop, and restart.
- Complete model-role application.
- Complete proxy log requests.
- Complete cancellable backup inspection and one-time inspection identifiers.
- Complete every in-scope mutation operation:
  - account detect, login, import, export, backup-all, backup-export, backup-import, save-current,
    save, edit, refresh, remove, and clear;
  - gateway create, test, edit, refresh, and remove;
  - client reset;
  - Hub source toggle;
  - proxy restart-all and stop-all;
  - launch-at-login, auto-start-proxies, show-quota, and quota-guard settings.
- Complete snapshots for accounts, native sources, login methods, gateways, clients, bindings,
  model actions/roles, proxies, pools, Hub routes/conflicts, usage, activity, backups, settings, and
  degraded conditions.
- Complete cancellation ownership so cancelled commands cannot publish a late success.
- Complete supervisor singleton/PID ownership, native process lifecycle, enabled-proxy startup,
  stop cleanup, signal forwarding, and crash recovery.
- Keep `src/macos/` and the Tauri client as protocol consumers; do not duplicate UI-specific
  business rules in Go.

Acceptance gate:

- No in-scope operation returns `not supported by the Go engine yet`.
- Go passes the same protocol fixtures used by TypeScript.
- Swift design checks, macOS feature-parity tests, Tauri UI checks, and packaged tray smoke pass.
- A stale snapshot action is rejected without mutation.
- A cancelled login/backup operation rolls back and emits exactly one terminal result.

## Milestone 10: CI, documentation, packaging, and cutover readiness

Dependencies: Milestones 0 through 9.

Deliverables:

- Update `go/README.md` to describe the complete implementation accurately.
- Update CLI, account, proxy, Hub, pool, plugin, backup, tray, and troubleshooting documentation.
- Do not document TUI as implemented by Go.
- Add Go formatting, vet, normal test, race test, build, and smoke gates to CI.
- Keep cross-runtime contract fixtures checked by both TypeScript and Go jobs.
- Inject the package version into the Go binary reproducibly, for example through `-ldflags`.
- Add package-level smoke coverage for the Go binary and its optional Node plugin host.
- Verify source builds and packaged builds separately.
- Keep TypeScript as the default released implementation until the final definition of done passes.

Cutover is a separate maintainer decision. Do not change the npm `bin` entry, delete the TypeScript
implementation, or remove the fallback merely because the Go parity suite passes.

Acceptance gate:

- CI enforces every final validation command that is portable to its runner.
- Documentation claims are exercised by smoke or golden tests.
- A clean packaged install passes CLI, proxy, plugin-host, and desktop protocol smoke tests.

## Mandatory execution loop

The implementing agent must not stop after planning, scaffolding, compiling, or passing only focused
tests. Repeat the following loop until every in-scope `go/PARITY.md` row is complete:

1. Read `AGENTS.md`, this plan, and `go/PARITY.md`.
2. Select the earliest incomplete milestone whose dependencies are complete.
3. Read the corresponding TypeScript source and tests.
4. Add or update Go contract/regression tests.
5. Implement the smallest complete vertical slice.
6. Run focused tests for that slice and fix every failure.
7. Review the slice against TypeScript behavior and the non-negotiable invariants.
8. Run the full Go milestone validation.
9. Update `go/PARITY.md` with implementation and test evidence.
10. Continue to the next incomplete item without asking for routine confirmation.

Ask the user only when a decision would materially change public behavior, requires destructive
action, or cannot be resolved from repository evidence.

After context compaction or session continuation, conversation memory is not authoritative. Reread
`AGENTS.md`, this file, and `go/PARITY.md` before editing.

## Mandatory self-review pass

After implementation appears complete, stop adding features and review the work as if it were
another maintainer's pull request. Review every changed and newly created file, including untracked
files that do not appear in `git diff`.

Check for:

- Missing TypeScript commands, options, defaults, or protocol operations.
- Incorrect capability pairing or compatibility inferred from registry membership.
- Business logic in CLI/desktop handlers instead of services.
- SQL outside stores or persisted mutations outside coordinator locks.
- Confusion between mutation scope and precise resource identity.
- Planner/executor divergence or rollback gaps.
- Race conditions, leaked goroutines, orphan children, or missing cancellation/timeouts.
- Unbounded input, file reads, response bodies, concurrency, or retry loops.
- Non-loopback proxy listeners or missing authentication.
- Credentials in stdout, JSON, errors, logs, state snapshots, or child arguments.
- Non-atomic snapshot, import, backup, config, or route-manifest writes.
- TypeScript-incompatible JSON, DB, backup, or stdio shapes.
- Provider-specific branching in core, CLI, or desktop code.
- Incorrect error codes, suggestions, mutation flags, or exit codes.
- Tests that assert an implementation detail while missing user-visible behavior.
- Help, completion, docs, and packaged behavior drift.

Run and classify every result of:

```bash
rg -n \
  'not supported by the Go engine yet|not yet ported|tracked separately|TODO|FIXME' \
  go
```

Intentional unsupported transport pairs and nested presets are allowed only when TypeScript has the
same behavior and a test documents it. Fix every actionable review finding, then rerun the affected
focused tests.

## Final validation

Run `gofmt` on every changed Go file, then execute these gates from the final working tree:

```bash
cd go && go vet ./...
cd go && go test ./... -count=1
cd go && go test -race ./... -count=1
cd go && go build ./cmd/anypick
cd go && ./scripts/smoke.sh
pnpm check
pnpm macos:design:check
pnpm tray:check
cd docs && pnpm build
git diff --check
git status --short
```

Run platform-specific packaged tray smoke wherever the required native toolchain is available.

If a validation command modifies files, review those changes and restart final validation from the
first command. If a command fails:

1. Diagnose the root cause.
2. Fix it without suppressing the failure.
3. Run the focused regression test.
4. Restart the complete final validation sequence.

A skipped, unavailable, timed-out, flaky, or unexecuted required command is not a pass. Record it as
a blocker and do not claim completion.

## Final definition of done

Do not report completion while any of the following is true:

- An in-scope `go/PARITY.md` row is `missing`, `partial`, `blocked`, or unverified.
- An in-scope CLI command or desktop protocol operation is unsupported.
- Go and TypeScript cannot exchange DB state, account exports, backups, bindings, or route manifests.
- A required validation command has not passed on the final working tree.
- The self-review has an unresolved correctness, security, compatibility, concurrency, or lifecycle
  finding.
- Documentation describes behavior the Go implementation does not provide.

The final report must include:

1. Completed milestones.
2. Important architecture decisions and compatibility tradeoffs.
3. Changed and new files grouped by subsystem.
4. Security, concurrency, rollback, and backward-compatibility review results.
5. A table of every final validation command and its result.
6. Every intentional difference from TypeScript.
7. Remaining in-scope work, which must be `none` to claim parity.

