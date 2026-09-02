# Architecture decision records

Each ADR states one decision, the context that forced it, and the consequences
that follow. They are the answer to "why is it like this?" and are the right
thing to cite from a code comment — they are short, numbered stably, and
immutable once accepted.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-test-typecheck-resolution.md) | Tests are typechecked under a `bundler`-resolution config that mirrors how Vitest and the Vite SSR build actually consume extensionless source. | accepted |
| [0002](0002-account-import-trust-boundary.md) | Imported account envelopes are decoded from `unknown` by a versioned, I/O-free codec; every file key is validated for traversal before any mutation. (SEC-01) | accepted |
| [0003](0003-process-lease-ownership.md) | A spawned process is owned only if it echoes the `instanceId` AnyPick gave it; a live PID is never ownership proof. (PROC-01) | accepted |
| [0004](0004-atomic-snapshot-save.md) | `writeMeta` is the single atomic commit point for a snapshot; a failed backup or import can never destroy the previous saved account. (DATA-01) | accepted |
| [0005](0005-ephemeral-scoped-lifecycle.md) | An ephemeral run is one scoped lifecycle: the executor builds the isolated home exactly once, and the CLI launcher — not the executor — spawns the child. (RUN-01) | accepted |
| [0006](0006-proxy-authentication.md) | Every local credentialed proxy request is authenticated with a per-instance high-entropy secret; loopback alone is not a boundary. (PROXY-01) | accepted |
| [0007](0007-project-binding-scope.md) | A project binding writes project-scoped metadata only, never global client config or proxy state. (SCOPE-01) | accepted |
| [0008](0008-durable-recovery-storage.md) | Crash backups live in an owner-only recovery dir under the AnyPick root, keyed to avoid collisions — not in system temp. (TXN-01) | accepted |
| [0009](0009-mutation-coordinator.md) | Every persisted mutation runs under the internal coordinator with sorted, scoped locks; correctness never depends on the caller locking. (CONC-01) | accepted, amended by [0011](0011-reentrant-provider-scoped-locks.md) |
| [0010](0010-observable-degraded-state.md) | Degraded and lifecycle conditions are emitted through an injectable, redacting event port instead of being swallowed in `catch`. (OBS-01) | accepted |
| [0011](0011-reentrant-provider-scoped-locks.md) | Account mutations lock one re-entrant `provider/<id>` scope covering the live credential file and every snapshot; activation maps its source ref onto the same scope. (CONC-02) | accepted |
| [0012](0012-plugin-trust-boundary.md) | Plugins are installed disabled, enabled by an explicit decision, pinned to a digest verified before `import()`, activated inside the sealing window, and never fatal. (EXT-01) | superseded by [0021](0021-plugin-host-removed-from-native-builds.md) — the registry and the digest survive; the execution grant does not |
| [0013](0013-model-routed-proxy-hub.md) | One loopback Proxy Hub keeps provider model IDs unchanged, routes exact IDs only after explicit collision resolution, and gives each client a token-scoped manifest. (HUB-01) | accepted |
| [0014](0014-plugin-package-digest.md) | The plugin trust pin is a SHA-256 of the whole package (not only `main`), verified before `import()`. (EXT-02) | superseded by [0021](0021-plugin-host-removed-from-native-builds.md) — the pin is still computed and still verified; there is no import to gate |
| [0015](0015-declarative-compatible-provider-plugins.md) | Compatible provider plugins declare official endpoints and credential products; core owns snapshots, dual-protocol proxying, pools, and Hub backends. (EXT-03) | accepted, amended by [0017](0017-provider-owned-browser-login.md) and [0021](0021-plugin-host-removed-from-native-builds.md) — the mechanism now ships as built-ins |
| [0016](0016-compatible-backend-capability-routing.md) | Compatible providers use a credential-aware backend and route native protocol surfaces only when every pool candidate declares the capability. (EXT-04) | accepted |
| [0017](0017-provider-owned-browser-login.md) | Providers own browser login methods; official methods are preferred, not required. (AUTH-01) | accepted, amends [0015](0015-declarative-compatible-provider-plugins.md) |
| [0018](0018-macos-app-single-swift-tree.md) | macOS is one Dock app compiled from `src/macos/`. Fixture vs live is a data source, not a second Swift tree. | accepted |
| [0019](0019-native-app-cutover-architecture.md) | The native app owns lifecycle over a bundled Go service on bounded, versioned stdio; host effects are narrow typed requests; the Node plugin-host and npm channel are documented exceptions. | superseded by [0020](0020-node-free-native-app-distribution.md) |
| [0020](0020-node-free-native-app-distribution.md) | Native downloads are full desktop applications over one bundled Go engine, contain no Node runtime, and treat npm only as an optional distribution adapter. | accepted |
| [0021](0021-plugin-host-removed-from-native-builds.md) | Native builds install plugins but never execute them: the registry and the whole-package digest survive, no third-party code is loaded, and every candidate degrades to a visible `LoadFailure`. (EXT-05) | accepted |

## Writing a new one

Copy the shape of an existing record: `Status`, `Context` (what was wrong or
undecided), `Decision` (numbered, concrete), `Consequences`. Number it with the
next four-digit ordinal. Do not edit an accepted ADR to reflect new thinking —
write a new one and mark the old one superseded, naming its replacement.

The older planning documents in [`../docs/history/`](../docs/history/) are not
ADRs. They are proposals and narratives, superseded in places, kept only for
provenance.
