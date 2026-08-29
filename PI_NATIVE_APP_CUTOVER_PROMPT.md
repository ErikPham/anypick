# AnyPick Native App and Go Service Cutover — Pi Coding Agent Prompt

> **Superseded architecture notice (2026-08-26):** ADR 0020 supersedes ADR
> 0019 and every clause below that retains a private Node plugin host, the Ink
> TUI, or another Node product runtime. The current authority is
> `adr/0020-node-free-native-app-distribution.md` plus
> `NATIVE_APP_CUTOVER.md`: native downloads are full desktop applications over
> one bundled Go core and contain no Node runtime; npm is only a thin optional
> distribution adapter to the same platform binary. This file remains as
> implementation-history provenance and must not override those sources.

## Role

Act as the senior maintainer responsible for taking AnyPick from its current
Node-owned desktop/tray architecture to a production native application:

- SwiftUI app on macOS;
- Tauri app on Windows and Linux;
- Go as the single application service and native CLI engine;
- native installers contain no Node runtime and require no Node.js, Go, Rust,
  Swift, Xcode, or other developer toolchain on the user's machine;
- npm remains available only as a convenience channel to the same platform Go
  binary.

This is an implementation assignment, not a proposal-only assignment. Inspect,
design, test, implement, audit, fix, and continue until the definition of done
is satisfied. Do not stop after scaffolding, compiling one target, or making a
tracker look complete.

## First actions on every session or context continuation

Before editing anything, read these files completely:

1. `AGENTS.md`
2. `CONTRIBUTING.md`
3. this file
4. `go/PARITY_PLAN.md`
5. `go/PARITY.md`
6. `src/macos/README.md`
7. `src/core/app.ts`
8. `go/internal/app/app.go`
9. the current persistent cutover tracker created by Milestone 0

Conversation memory is not authoritative after compaction. Reread the files and
continue from verified repository state. Inspect untracked files as well as
tracked diffs.

Do not use `docs/history/` as a current specification. It is provenance only.

## Objective

Ship one product with three shells/channels over one Go engine:

```text
macOS SwiftUI app                 Windows/Linux Tauri app
        |                                  |
        +-------- versioned stdio ---------+
                           |
                 bundled AnyPick Go service
          accounts / bindings / runtime / proxies
          pools / Hub / backup / doctor / plugins
                           |
                   ~/.anypick + client files

native CLI ---------------- same Go core in-process
npm shim ------------------- platform Go binary
first-party extensions ------ compiled into Go
compatible extensions ------- declarative data decoded by Go
```

The application owns lifecycle. The menu-bar/status-area item is an auxiliary
surface of the application, not the product owner and not the parent process.

## Product decisions already made

Treat these as requirements unless repository evidence shows an unavoidable
correctness or security conflict. Ask the user only for such a conflict.

1. The macOS product is a regular Dock app with a main window, Settings scene,
   and `MenuBarExtra`. Closing the main window does not quit the app or stop
   services. The menu bar is a compact power surface.
2. SwiftUI owns macOS presentation and OS integration. Tauri/Rust owns the same
   concerns on Windows/Linux. Business logic does not move into either shell.
3. Go owns domain logic, persistence, activation, proxying, Hub, pools, account
   transfer, settings policy, diagnostics, and desktop command execution.
4. The app launches and supervises the bundled Go service. Do not make Go spawn
   the app in production.
5. Use bounded, versioned stdio IPC for the first cutover. Do not introduce a
   permanent daemon, localhost HTTP API, Unix socket, named pipe, gRPC, or a new
   serialization framework without an accepted ADR and a demonstrated need.
6. Reuse and complete the existing desktop protocol before replacing its frame
   format. Add a handshake and capability negotiation without gratuitously
   rewriting every DTO.
7. Native installers and their installed trees contain no Node runtime or
   `plugin-host.mjs` and must run with `node`, `go`, `rustc`, `swiftc`, and
   `xcrun` absent from `PATH`.
8. Executable ESM plugins are not a native-release requirement. First-party
   extensions compile into Go; compatible extensions are declarative and
   core-owned. Do not add a private Node runtime to preserve ESM compatibility.
9. npm users already have Node by definition, but npm is only a distribution
   adapter. Its minimal launcher must execute the platform Go binary for every
   supported command. Port required TUI behavior to Go or retire it through an
   explicit semver-major migration; do not retain a second production engine.
10. Initial supported desktop release matrix:
    - macOS 14+ arm64 and x86_64, preferably one universal app;
    - Windows 10/11 x64;
    - Linux x64 with an explicitly documented minimum distribution/runtime
      baseline.
    Arm64 Windows/Linux may be added after the initial matrix is genuinely
    tested; do not claim it merely because Go cross-compiles.
11. Initial distribution targets:
    - GitHub Releases for every artifact;
    - signed and notarized macOS DMG;
    - Homebrew Cask for the macOS app and Homebrew Formula for CLI-only;
    - signed Windows NSIS installer and WinGet manifest;
    - Linux AppImage, `.deb`, and `.rpm`;
    - npm package and platform binary packages.
    Mac App Store, Microsoft Store, Flatpak, Snap, and AUR are later channels
    unless all current gates are complete first.
12. Do not publish, upload, tag, push, notarize with production credentials, or
    submit Homebrew/WinGet changes externally without explicit user approval.
    Build the automation and verify dry-run/local artifacts instead.

## Architecture boundaries

### Go service

Use one composition root and one behavior implementation. The same internal Go
services back:

- the native CLI;
- the desktop stdio service;
- hidden proxy, pool, and Hub children;
- npm's platform-native command execution.

The desktop service should be a hidden command such as
`anypick desktop-service --stdio`. Its stdout is protocol-only. Diagnostics go
to stderr and must be sanitized. It must expose a version handshake before
accepting mutation commands.

Do not copy business behavior into `cmd/anypick`, `internal/desktop`, Swift, or
Tauri. Handlers parse and validate transport input, call services, and encode
results. Services own mutation coordination. Stores own SQL.

### Platform host effects

Some operations cannot and should not be pure Go. Model them as narrow host
effects instead of duplicating business logic:

- launch at login;
- open file/save panels;
- open browser or System Settings;
- notifications;
- Dock/taskbar/window/menu behavior;
- installer/updater integration.

The Go service may request a typed host effect and wait for a typed result. The
Swift or Rust host executes only the platform operation. It must not resolve
accounts, choose transports, construct activation behavior, or mutate AnyPick
persistence independently.

### Process lifecycle

- Swift/Tauri launches the exact helper inside its signed bundle; never search
  `PATH` for the service.
- Perform protocol/app/engine version handshake before the first snapshot.
- Reject incompatible versions with a useful degraded screen, not undefined
  behavior.
- Supervise unexpected exits with bounded exponential backoff and a maximum
  restart count.
- On app quit, request graceful service shutdown and wait with a timeout before
  terminating the child.
- Closing a window is not app quit.
- Do not add an OS-level daemon in the first release. If persistent proxies
  after explicit app Quit become a product requirement, write an ADR first.

### Data and rollback

- `~/.anypick` and `ANYPICK_HOME` remain authoritative.
- Go and TypeScript must remain mutually compatible with released schema,
  backups, account envelopes, bindings, presets, profiles, pools, and Hub route
  manifests through the cutover window.
- Refuse a newer schema before writing.
- The first Go-native release must have tested upgrade and rollback paths.
- Never dual-run mutations against Node and Go. Read-only comparison is allowed;
  writes have exactly one owner.

## Non-negotiable invariants

Preserve every invariant in `AGENTS.md` and `go/PARITY_PLAN.md`, especially:

1. Capability pairs come from `transportFor` semantics.
2. Proxy and plugin secrets never cross public output boundaries.
3. Activation is planned, journalled, and executed from `ActivationPlan.steps`.
4. `writeMeta` remains the atomic account-snapshot commit point.
5. Every persisted mutation runs under sorted coordinator locks.
6. Account mutations use provider scope, not account identity scope.
7. No prompt without a TTY; JSON stdout remains exactly machine-readable.
8. Proxy listeners are loopback-only and every credentialed request is
   authenticated.
9. Plugin digest verification happens before importing plugin code.
10. Platform shells never receive raw proxy tokens or saved credentials in
    snapshots.
11. Bundled helpers are located by trusted bundle-relative paths and checked
    for expected version/digest before use.

## Persistent tracker

### Milestone 0 requirement

Create `NATIVE_APP_CUTOVER.md` in the repository root before implementation.
Keep it current after every vertical slice. It is the durable source of truth
for future Pi sessions.

Use these statuses only:

- `missing`
- `partial`
- `complete`
- `blocked`

Each row must include:

| Field | Required content |
| --- | --- |
| Work item | Stable user-visible, protocol, packaging, or operational behavior |
| Authority | TypeScript/Go/Swift/Tauri source and tests defining current behavior |
| Implementation | Exact new source paths |
| Focused verification | Exact tests/commands |
| End-to-end verification | Installed or cross-process test proving reachability |
| Platforms | macOS, Windows, Linux, npm as applicable |
| Status | missing, partial, complete, blocked |
| Notes | Remaining gaps, risk, intentional difference, or artifact evidence |

A row is `complete` only when behavior is reachable through the shipped surface,
the focused tests pass, the relevant end-to-end test passes, and the artifact
contains the required files. A package, type, mock, command name, or isolated
unit test alone is not completion.

Keep `go/PARITY.md` synchronized. Go parity is a dependency of native cutover,
not a parallel definition of done that may remain incomplete.

## Required implementation rhythm

For every vertical slice, use this exact loop:

1. Select the earliest incomplete tracker row whose dependencies are complete.
2. Read its behavioral authority and existing tests.
3. State the expected behavior and failure modes in the tracker notes or test
   name before implementing.
4. Add or update a focused regression/contract test that fails for the missing
   behavior when practical.
5. Implement the smallest complete vertical slice.
6. Run focused formatting, static analysis, and tests.
7. Perform a reviewer pass against the diff and the authority. Look for data
   loss, wrong scope, secret leakage, protocol drift, cancellation, lifecycle,
   race, and packaging mistakes.
8. Fix every actionable review finding and add regression coverage.
9. Run the milestone gate, including at least one cross-process or artifact
   test.
10. Update both trackers with exact evidence.
11. Continue automatically to the next eligible row.

Do not ask for routine confirmation. Ask only when a choice materially changes
public behavior, requires destructive action, needs production signing or
publishing authority, or cannot be resolved from repository evidence.

## Implementer, auditor, and test coordination

If Pi provides subagents or parallel workers, use them only for bounded tasks
with non-overlapping write ownership:

- **Implementer:** owns one subsystem/vertical slice and its focused tests.
- **Contract auditor:** read-only comparison with TypeScript/protocol/platform
  authority; reports concrete findings with paths and severity.
- **Test/release auditor:** runs independent gates and inspects the produced
  artifact, without editing implementation files while tests run.

Do not have multiple agents edit the same files concurrently. The primary agent
integrates findings, applies fixes, and reruns the affected tests. A subagent
saying “looks good” is not evidence; require commands, artifacts, or specific
source comparison.

When `[NATIVE_CUTOVER_ORCHESTRATION_START]` or
`[NATIVE_CUTOVER_ORCHESTRATION_CONTINUE]` is present, subagents are mandatory.
Use the project agents named by `.pi/APPEND_SYSTEM.md`; if the `subagent` tool or
those agents are unavailable, report the concrete setup failure and end with
`[CUTOVER:BLOCKED]`. Never describe a same-context self-review as external or
independent review.

Outside the orchestrated workflow only, if no subagents are available, perform
the same roles as separate passes. After implementation, clear your assumptions,
reread the diff as a self-review pass, then run tests from the final tree.

## Milestone 0 — Freeze target contract and baseline

### Deliverables

- Create `NATIVE_APP_CUTOVER.md` with a complete inventory covering every
  milestone below.
- Write an ADR for:
  - app-owned lifecycle;
  - bundled Go service over bounded stdio;
  - no first-release OS daemon;
  - platform host-effect boundary;
  - Node-free native artifact boundary and executable-plugin migration policy;
  - native/npm distribution relationship.
- Record the current architecture and exact ownership inversion points:
  - Node supervisor currently compiles/spawns Swift;
  - Go `app run` currently spawns the app;
  - Tauri currently reads/writes the parent supervisor's stdio.
- Add a reproducible performance baseline script/report for:
  - CLI cold and warm startup;
  - max RSS;
  - service startup to first snapshot;
  - idle CPU/RSS;
  - proxy request and streaming first-byte overhead;
  - artifact size.
- Record current local reference measurements without turning them into hard
  promises: stripped Go binary is approximately 11 MB; the existing warm CLI
  sample was roughly twice as fast and used less than half the max RSS of the
  Node CLI. Re-measure in CI/reference hardware.
- Define the first supported platform matrix and minimum OS/runtime versions.

### Acceptance gate

- Every in-scope behavior and artifact has a tracker row.
- Architecture decisions do not conflict with accepted ADRs or root invariants.
- Baselines are reproducible and report machine/OS/toolchain metadata.
- No production behavior changes in this milestone.

## Milestone 1 — Make the Go engine cutover-safe

### Dependencies

Milestone 0.

### Required work

Complete every Go-owned product row of `go/PARITY.md`. Arbitrary JavaScript
plugin parity and the TypeScript Ink TUI are not native-release scope under ADR
0020.
At minimum resolve all previously identified high-severity issues:

- Desktop backup export/import protocol must preserve `accounts`,
  `inspectionId`, `entryIds`, and `overwriteConfirmed`; selection must never
  expand silently to all credential-bearing accounts.
- `link`, `unlink`, and `reset` must match TypeScript project/global binding
  semantics.
- `proxy pool disable` must disable rather than call the enabling store path;
  strategy/mode flags must be implemented or rejected explicitly.
- All import and restore mutations must run through service-owned, sorted,
  provider-scoped coordinator locks.
- Proxy Hub activation and pool transport must be real end-to-end behavior, not
  surface commands that return unavailable.
- Complete CLI command tree, global flags, JSON behavior, version injection,
  help golden tests, and completion.
- Complete settings, login, cancellation, snapshot, quota guard, protocol,
  plugin, and packaging parity defined by `go/PARITY_PLAN.md`.
- Preserve the completed Node plugin-host security/recovery work only as
  migration provenance while removing it from production composition and
  release artifacts.

Do not implement these fixes only in CLI or desktop handlers. Port/complete the
service layer so CLI and desktop use identical behavior.

### Acceptance gate

- Every in-scope `go/PARITY.md` row is `complete`.
- Go and TypeScript alternate over the same fixture data root successfully.
- Cross-runtime account, backup, DB, binding, profile, pool, Hub, protocol, and
  error fixtures pass.
- `go test -race ./... -count=1` passes.
- The Go binary reports an injected release version and refuses protocol/schema
  versions it cannot safely handle.
- No known P0/P1 correctness, security, concurrency, or lifecycle finding
  remains.

## Milestone 2 — Stabilize the app service contract

### Dependencies

Milestone 1.

### Deliverables

- Add a hidden Go `desktop-service --stdio` entry backed by the normal
  application composition root.
- Add startup handshake containing:
  - protocol version;
  - app/engine version;
  - supported command/snapshot capabilities;
  - data schema version;
  - a non-secret process instance identifier.
- Reject mismatched app/helper versions before mutations.
- Make all command results exactly-once, including cancellation and service
  shutdown races.
- Replace fixed full-snapshot polling where practical with:
  - initial snapshot;
  - snapshot/event after mutation;
  - bounded scheduled refresh only for externally changing data such as usage;
  - explicit refresh command.
- Add typed host-effect request/result messages for platform-only operations.
- Keep protocol frames bounded and canonical. Malformed input must be ignored or
  rejected deterministically without killing the service.
- Add a fake-service harness usable by Swift and Tauri UI tests.
- Add a real cross-process harness that spawns the Go service and exercises all
  commands against a temporary `HOME` and `ANYPICK_HOME`.

### Acceptance gate

- One protocol fixture set is consumed by Go, Swift, and Tauri/Rust.
- Every in-scope desktop command has success, structured failure, cancellation,
  stale revision, malformed input, and incompatible-version coverage where
  applicable.
- stdout contains protocol frames only; stderr and snapshots contain no secret.
- Killing and restarting the service does not corrupt state or orphan owned
  children.

## Milestone 3 — Make `src/macos` a first-class application

### Dependencies

Milestone 2.

### Preserve

- The current System Settings-style main window and information architecture in
  `src/macos/README.md`.
- `Window`, `Settings`, and `MenuBarExtra` scenes.
- The menu bar as a compact quick-control surface.
- Fixture and production using the same Swift view tree.

### Deliverables

- Add a real Xcode project/workspace and schemes under `src/macos` without
  creating a second Swift source tree.
- Add production `Info.plist`, entitlements, asset catalogs/resources, unit-test
  target, UI-test target, and archive configuration.
- Build/package the Go helper into `Contents/Helpers` through a deterministic
  build phase or pre-archive script.
- Replace parent-owned stdin handling with an app-owned `GoServiceProcess`
  actor/client:
  - bundle-relative executable resolution;
  - pipe ownership;
  - handshake;
  - async read/write;
  - bounded restart policy;
  - graceful termination;
  - degraded UI state.
- Refactor `TrayStore` into an app/desktop protocol store. Separate transport,
  state reduction, and feature coordination.
- Reduce `ProductionDesignModel` responsibility. Move domain validation and
  mutation policy to Go; keep presentation mapping in Swift.
- Remove production runtime compilation through Node/`xcrun swiftc`.
- Keep menu-bar preview windows fixture/debug-only.
- Implement launch at login with `SMAppService`. Normal manual launch opens the
  main window; login launch starts quietly with the menu bar available.
- Make Settings reflect requested and actual platform state, including denied
  login-item authorization.
- Ensure app reopen, Dock activation, close-window, Settings, menu-bar open,
  service crash, and app quit have deterministic lifecycle tests.

### Signing/package preparation

- Support arm64 and x86_64; produce and test a universal app if practical.
- Sign the nested Go helper before signing the outer app.
- Enable Hardened Runtime with the minimum entitlements.
- Do not grant JIT entitlements; the native artifact contains no JavaScript
  runtime.
- Add automated verification commands for `codesign`, `spctl`, notarization log,
  and stapled ticket. CI without credentials may use ad-hoc signing but must
  validate bundle structure.

Reference current Apple requirements when implementing release automation:

- https://developer.apple.com/documentation/servicemanagement/smappservice
- https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution
- https://developer.apple.com/documentation/security/hardened-runtime

### Acceptance gate

- The archived `.app` contains the expected Go helper and resources.
- A clean macOS user account launches the installed app with Node, Go, Rust,
  Swift, and Xcode tools absent from `PATH`.
- Main window, Settings, MenuBarExtra, login launch, service crash recovery,
  account switch, proxy lifecycle, backup flow, and app quit pass installed-app
  smoke tests.
- Closing the main window leaves the app/service operational.
- No production code compiles Swift sources on the user's machine.
- The ad-hoc artifact passes bundle/signature structure checks; production
  release remains blocked until real Developer ID signing and notarization pass.

## Milestone 4 — Establish and enforce performance budgets

### Dependencies

Milestones 1 through 3.

### Deliverables

- Add repeatable benchmarks for Go services and end-to-end app/service startup.
- Compare the last Node service release and Go service against identical fixture
  data; never compare unrelated commands or data roots.
- Measure p50/p95 and variance, not one favorable run.
- Record at least:
  - CLI help/version/read-only startup;
  - app launch to first usable snapshot;
  - idle app and service CPU/RSS;
  - 100-account snapshot construction and decode;
  - proxy non-streaming latency overhead;
  - streaming first-byte overhead;
  - pool failover/round-robin overhead;
  - Hub routing throughput;
  - installed and compressed artifact sizes.
- Add fast help/version paths that do not initialize DB or run maintenance.
- Remove polling, repeated full-file reads, or per-account process cost only
  where measurement proves it matters. Do not rewrite lifecycle architecture
  merely to improve a micro-benchmark.

### Initial budgets

Treat these as regression budgets to validate and adjust with evidence:

- warm CLI read-only p95: <= 100 ms;
- service spawn to compatible handshake p95: <= 500 ms;
- app launch to usable snapshot p95: <= 2 seconds on reference hardware;
- idle Go service CPU: <= 0.2%;
- idle Go service RSS: <= 40 MB for a small fixture;
- local proxy p95 additive overhead: <= 10 ms excluding upstream latency;
- no unbounded growth over repeated refresh/mutation/cancel cycles.

### Acceptance gate

- Benchmarks run in CI or a documented stable performance job.
- Results and reference-machine metadata are retained as artifacts.
- No budget regression is hidden by increasing a threshold without a written
  explanation.
- Performance work preserves correctness, cancellation, secret boundaries, and
  rollback behavior.

## Milestone 5 — Turn Tauri into the Windows/Linux app

### Dependencies

Milestones 2 and 4. Milestone 3 provides the app-owned lifecycle reference.

### Deliverables

- Change product identity from `AnyPick Tray` to `AnyPick` with stable package
  identifiers.
- Normal launch shows a resizable main application window and taskbar entry.
  Background/login launch may start hidden with the status item available.
- Preserve the tray/status item for quick actions; detailed configuration lives
  in the main app.
- Move the Tauri tree toward desktop-app naming after behavior is stable. Avoid
  a rename-only mega-diff before the cutover works.
- Bundle the target Go binary using Tauri `externalBin` and launch it from Rust.
  The React UI must not spawn shell commands directly.
- Remove the architecture where Tauri reads its own stdin from a Node parent and
  writes commands to stdout for Node.
- Reuse the shared protocol fixtures and fake/real service harnesses.
- Port Go platform primitives:
  - Windows coordinator/file locking;
  - child process groups/Windows Job Objects or equivalent ownership;
  - graceful and forced termination;
  - PID ownership checks without Unix signals;
  - Windows credential and client configuration paths;
  - filesystem permissions/ACL expectations;
  - Linux process, credential-store, XDG, and packaging behavior.
- Keep proxy listeners loopback-only on all platforms and test IPv4/IPv6
  behavior explicitly.
- Add Windows and Linux CI jobs that build on the target OS. Do not call a
  cross-compiled executable supported until installed-app tests pass there.

Reference current Tauri requirements:

- https://v2.tauri.app/develop/sidecar/
- https://v2.tauri.app/distribute/
- https://v2.tauri.app/distribute/windows-installer/

### Platform artifacts

- Windows x64: signed NSIS installer; MSI may be added after the NSIS path is
  reliable.
- Linux x64: AppImage, `.deb`, and `.rpm`, built on the oldest supported base
  system compatible with the selected Tauri/WebKitGTK versions.
- Include the exact Go sidecar and frontend built from the release commit.

### Acceptance gate

- `GOOS=windows GOARCH=amd64` and Linux release builds pass from the final tree.
- Installed Windows and Linux apps work with Node and Go absent from `PATH`.
- Main window, tray/status item, service restart, account switch, proxy, backup,
  settings, update check, and quit pass platform smoke tests.
- Windows artifact is code-signed for production; unsigned development builds
  are never presented as release-ready.
- Linux minimum runtime/distribution support is documented from tested evidence.

## Milestone 6 — Prove native release artifacts contain no Node runtime

### Dependencies

Milestones 1, 3, and 5.

### Deliverables

- Remove every production-native path that invokes `node`, `npm`, `pnpm`,
  `tsx`, `swiftc`, `go`, or `cargo` from the user's environment.
- Remove private Node lookup, `plugin-host.mjs`, plugin-host environment
  injection, and JavaScript-plugin activation from macOS and Tauri production
  composition.
- Keep first-party providers/clients compiled into Go and compatible extensions
  declarative and core-owned.
- Add an artifact scan that fails on Node executables/scripts, production launch
  references to developer absolute paths, or toolchain expectations on `PATH`.

### Acceptance gate

- Clean native-install tests pass with a deliberately restricted `PATH`.
- The installed artifact itself contains no Node runtime or plugin-host script.
- Account, activation, proxy, pool, Hub, backup, settings, and quit flows work
  through the bundled Go core.

## Milestone 7 — Keep npm without keeping a second service engine

### Dependencies

Milestones 1, 2, and native Go release binaries.

### Deliverables

- Keep the root `anypick` npm package as an optional installation channel.
- Use the minimum JavaScript launcher required to locate and execute the correct
  platform Go binary for every supported command.
- Port required TUI behavior to Go or retire it, together with unsupported
  public JS APIs, through an explicit semver-major migration.
- Publish platform packages selected through npm `os`, `cpu`, and `libc`
  metadata, for example:
  - `@anypick/cli-darwin-arm64`
  - `@anypick/cli-darwin-x64`
  - `@anypick/cli-linux-x64-gnu`
  - `@anypick/cli-win32-x64`
- Reference platform packages through `optionalDependencies`; avoid a
  postinstall downloader.
- Provide a deterministic error when optional dependencies were omitted or the
  platform is unsupported.
- Forward signals, stdio, exit codes, and arguments exactly through the npm
  launcher.
- Ensure `--json` remains one parseable stdout stream through the launcher.
- Keep npm trusted publishing/provenance and pack/consumer verification.
- Do not ship the obsolete Node service merely as a fallback after the native
  launcher is declared stable. Keep a fallback only during an explicitly
  documented beta window.

Reference npm package metadata behavior:

- https://docs.npmjs.com/files/package.json/

### Acceptance gate

- `npm install -g` and local `npm exec` smoke tests pass on each supported
  platform.
- Every supported command executes the release Go binary and matches
  direct-native CLI output/exit codes.
- Removed TUI/public-JS surfaces have explicit semver-major migration notes;
  any retained behavior is Go-backed.
- Installing with optional dependencies omitted produces a clear, non-secret
  error rather than downloading or executing unverified code.

## Milestone 8 — Build a multi-platform release pipeline

### Dependencies

Milestones 3, 5, 6, and 7.

### Release model

One version and one Git tag identify all artifacts. Extend the existing release
planner instead of adding an unrelated version system. Synchronize:

- `package.json`;
- Go `-ldflags` version metadata;
- Swift `MARKETING_VERSION` and build number;
- Cargo/Tauri versions;
- platform package versions;
- Homebrew/WinGet manifest versions.

### CI/release matrix

Build on the target OS:

- macOS arm64/x86_64 or a verified universal artifact;
- Windows x64;
- Linux x64 on the selected minimum-compatible base;
- npm/library/TUI and docs jobs.

Each job must:

1. build from a clean checkout and locked dependencies;
2. run focused and platform tests;
3. assemble the final installer/archive;
4. sign where required;
5. inspect the installed/bundled file tree;
6. run artifact smoke tests;
7. emit SHA-256 checksums and an SBOM;
8. upload immutable workflow artifacts for the final release job.

The final release job verifies digests and version consistency before any
external publication. Reruns must be idempotent and reject an existing artifact
with different bytes.

### macOS

- Sign nested helpers, then the app, then DMG/package.
- Use Developer ID and Hardened Runtime.
- Submit with `notarytool`, inspect the log, staple, and validate.
- Verify with `codesign --verify --deep --strict`, `spctl`, and `stapler`.

### Windows

- Sign executable and installer.
- Verify Authenticode signature and clean installation/uninstallation.
- Generate and validate a WinGet manifest, but do not submit it without user
  approval.

### Linux

- Build AppImage, `.deb`, and `.rpm`.
- Verify package contents, desktop entry, icons, binary permissions, install,
  upgrade, and uninstall behavior.
- Publish checksums/signatures with the GitHub Release.

### Homebrew

- Add repository-owned templates/tests for:
  - Cask installing `AnyPick.app` and exposing the bundled CLI;
  - Formula installing CLI-only native archives.
- Start with a project tap. Do not submit to Homebrew repositories without user
  approval.
- Verify `brew install`, `brew upgrade`, `brew uninstall`, and `brew audit` on
  produced artifacts.

Reference packaging authorities:

- https://docs.brew.sh/Cask-Cookbook
- https://learn.microsoft.com/windows/package-manager/package/repository

### Acceptance gate

- A dry-run release builds and verifies every artifact from one commit/version.
- No artifact needs a developer toolchain at runtime.
- Signing/notarization is either green or explicitly blocks production release;
  it is never skipped and called a pass.
- GitHub Release, npm, docs, Homebrew, and WinGet publication steps consume the
  already verified bytes rather than rebuilding.

## Milestone 9 — Cutover, cleanup, and final audit

### Dependencies

All prior milestones.

### Deliverables

- Make the Go engine the default service and non-TUI CLI path.
- Remove production Node supervisor ownership and runtime Swift compilation.
- Retain only the TypeScript code still required for TUI, public JS APIs,
  plugin authoring/host assets, frontend build, and compatibility tests.
- Remove stale tray-first naming and documentation after behavior is stable.
- Preserve backwards-compatible config/data migration and document rollback.
- Update README and maintained docs for app-first installation and each
  distribution channel.
- Do not edit `CHANGELOG.md`; use a release-quality Conventional Commit subject
  when the user later chooses to commit.

### Mandatory independent review

Review every changed and newly created file as if it came from an external PR.
Classify findings P0-P3 and fix all actionable P0/P1 findings. Check especially:

- app/service ownership accidentally reversed;
- business policy remaining in Swift/React/Rust/CLI handlers;
- persisted mutation without coordinator locks;
- planner/executor/rollback divergence;
- wrong binding or provider mutation scope;
- protocol fields silently dropped;
- late success after cancellation;
- child leaks, process-group mistakes, restart loops, or shutdown races;
- secret exposure in frames, snapshots, command lines, logs, crash reports, or
  installer metadata;
- untrusted helper lookup, plugin import before digest verification, or overly
  broad macOS entitlements/Tauri capabilities;
- non-loopback listeners or unauthenticated proxy requests;
- app close/quit/login-launch ambiguity;
- platform claims without installed-artifact evidence;
- npm wrapper argument, signal, exit-code, and JSON drift;
- release jobs rebuilding after verification;
- version mismatch among Go, Swift, Cargo/Tauri, npm, and manifests;
- docs that still describe a tray utility or user-installed Node requirement.

### Acceptance gate

- All rows in `NATIVE_APP_CUTOVER.md` and all in-scope rows in `go/PARITY.md`
  are `complete`.
- Remaining in-scope work is `none`.
- Every required final validation command passes on the final working tree.
- Clean installed artifacts pass on every supported platform.
- No unresolved P0/P1 review finding remains.

## Final validation matrix

Run from a clean final tree. If a command mutates tracked files, inspect the
change and restart validation from the first relevant gate.

### Repository and TypeScript/npm

```bash
pnpm check
pnpm package:smoke
pnpm tray:ui:check
pnpm tray:e2e:ui
cd docs && pnpm install --frozen-lockfile && pnpm build
```

### Go

```bash
cd go && test -z "$(gofmt -l .)"
cd go && go vet ./...
cd go && go test ./... -count=1
cd go && go test -race ./... -count=1
cd go && go build -trimpath ./cmd/anypick
cd go && ./scripts/smoke.sh
```

Also build every supported target with the exact release flags and injected
version. A compile is necessary but not sufficient for platform support.

### macOS

Run the exact commands added by the Xcode project and release scripts for:

- Swift unit tests;
- UI tests;
- fixture/design compile;
- archive;
- bundle-content verification;
- installed-app smoke;
- signing, notarization, and staple verification when credentials are present.

Keep `pnpm macos:design:check` until its coverage is replaced by equal or better
Xcode-backed checks.

### Tauri Windows/Linux

Run:

- Rust formatting and tests;
- frontend typecheck/build;
- shared protocol fixtures;
- Tauri build on the target OS;
- installed artifact smoke with restricted `PATH`;
- installer signature/package-content verification.

### Release/package integrity

Run:

```bash
git diff --check
git status --short
```

Then run the new release artifact verifier over every platform artifact,
checksum, SBOM, npm tarball, Homebrew definition, and WinGet manifest.

Do not treat `git diff` as covering untracked files. Enumerate and review every
untracked source, test, project, manifest, entitlement, script, and asset.

## Failure handling

When a gate fails:

1. Diagnose the root cause.
2. Add or strengthen a focused regression test.
3. Fix the implementation rather than weakening the test or threshold.
4. Run the focused test.
5. Rerun the milestone gate.
6. Restart the affected final-validation sequence.

A skipped, unavailable, flaky, timed-out, unsigned, unnotarized, or unexecuted
required gate is not a pass. Record it as a blocker.

## Avoid

- Do not rewrite the SwiftUI views merely to rename tray concepts.
- Do not add a generic distributed-service framework.
- Do not add a permanent daemon before a real product requirement exists.
- Do not expose a token-free localhost control API.
- Do not use registry membership to infer provider/client compatibility.
- Do not ship postinstall scripts that download unverified native binaries.
- Do not embed all platform binaries in one oversized npm package.
- Do not restore executable plugin compatibility through QuickJS, Bun, Node, or
  another embedded runtime. A future sandbox requires a new accepted ADR; WASM
  is the preferred direction to evaluate there.
- Do not claim Windows/Linux support from cross-compilation alone.
- Do not publish an unsigned Windows release or an unnotarized macOS release as
  production-ready.
- Do not add Mac App Store/Flatpak/Snap work while core release gates remain
  incomplete.
- Do not remove the legacy Node service path without Go parity and rollback
  coverage; once those gates pass, remove it rather than shipping two engines.
- Do not edit unrelated dirty-worktree changes.

## Progress updates

At the end of each milestone, report concisely:

1. completed tracker rows;
2. important architecture decisions;
3. changed files grouped by subsystem;
4. tests and artifact gates run with exact results;
5. audit findings found and fixed;
6. blockers or remaining rows.

Then continue automatically when no user decision is required.

## Final report requirements

Do not say “done”, “parity complete”, “release ready”, or equivalent unless the
final definition of done is true.

The final report must contain:

1. completed milestones and supported platform matrix;
2. final architecture and lifecycle ownership;
3. data compatibility and rollback evidence;
4. security review of secrets, IPC, helpers, plugins, signing, and listeners;
5. performance table with reproducible methodology;
6. artifact table with version, platform, architecture, size, checksum,
   signature/notarization status, and smoke result;
7. every final validation command and result;
8. intentional differences between native, npm, and TUI channels;
9. remaining in-scope work, which must be `none` to claim completion.

Until then, report the current milestone as a completed slice, not the overall
objective as complete.
