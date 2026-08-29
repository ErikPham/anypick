# Working on AnyPick

AnyPick points AI coding clients at AI accounts you already have. It rewrites
other tools' credential and config files and spawns local proxies, so a careless
change corrupts a developer's real logins. Bias toward verifying over assuming.

The engine is **Go** (`go/`): CLI, terminal UI and desktop service all run from
one bundled binary (ADR 0020). The macOS app is Swift (`macos/`). npm is a
distribution adapter — `bin/anypick.js` resolves the platform Go binary and
spawns it; it ships no TypeScript and needs no Node runtime to do real work.

`CLAUDE.md` and `GEMINI.md` are symlinks to this file — edit this one.
`docs/AGENTS.md` (symlinked the same way) covers the docs site, which has
different rules; `docs/AGENT.md` is the nimbus template's own authoring guide.

## Commands

`make test` is `go test ./...`. Nothing wraps it. Run `make help` for the full
list; the ones you will actually use:

```bash
make test                      # go test ./... -count=1
make test-race                 # the same under -race
make vet
make fmt-check                 # fails if anything is not gofmt-clean
make build                     # -> go/bin/anypick-dev

make run ARGS="providers"      # build, then run a command
make sandbox ARGS="providers"  # same, against a throwaway HOME + ANYPICK_HOME

make release-check             # VERSION and Xcode MARKETING_VERSION agree
make release-bump PART=patch   # also: make release-set VERSION=1.2.3
make artifact-check ROOT=path  # ADR 0020 §8: no Node runtime in the artifact

make app                       # open macos/AnyPick.xcodeproj
make app-test                  # xcodebuild unit tests
```

There is no root `package.json` and no pnpm workspace: this repository has no
JavaScript build and no `scripts/` directory — a `Makefile` and Go tooling are
all there is. The only Node project is `docs/`, the documentation site, with its
own lockfile.

CI (`.github/workflows/ci.yml`) runs five jobs: format/vet/version sync, Go
tests on Ubuntu, Go tests on Windows, the macOS app and helper contract, and
the docs site. Run the Go ones before you claim a change is done.

> **GOPATH trap.** `~/.zshrc` currently exports `GOPATH="$HOME/workspace/ocg/go"`,
> which is *another project's* module cache. Tests that shell out to `go build`
> (`cmd/anypick`'s `requireBuiltCLI`) then stall in module resolution and the
> package times out after many minutes — which looks exactly like a change you
> just broke. It cost hours once; check this before debugging any code.
>
> The Makefile pins `GOPATH := $(HOME)/go`, so `make` targets are always safe.
> If you run `go` directly, use `GOPATH=$HOME/go go test ./...` (warm once with
> `go mod download all`). The real fix is to delete that export from `~/.zshrc`:
> Go modules have not needed GOPATH since 1.16.

The docs site is a **separate** pnpm project with its own lockfile:

```bash
cd docs && pnpm install && pnpm dev    # site only
cd docs && pnpm build
```

Your shell's working directory does not persist between tool calls, so prefix
every docs command with `cd docs &&` in the same invocation or you will silently
run it against the wrong project.

## Where the authority lives

| Question | Read |
| --- | --- |
| How is this layered? What do I implement to add a client/provider? | `CONTRIBUTING.md` |
| How does everything wire together? | `go/internal/app/app.go` — the composition root |
| *Why* is it like this? | `adr/` — cite records by number |
| What did we consider before? | `docs/history/` — **superseded proposals, provenance only.** Not the current spec. |
| What do users believe? | `docs/src/content/docs/` and `README.md` |
| How must the macOS app look and behave? | `macos/README.md`. Ship native controls. `design.pen` is product IA/content (not pixel-perfect; Pen is not 100% native). |

Do not edit an accepted ADR to reflect new thinking; write a new one and mark the
old superseded.

## Invariants

Breaking one of these is a correctness or security regression, not a style nit.

1. **Capability pairs come from `TransportFor`** in `go/internal/transport`.
   `claude`, `codex`, and `opencode` may accept a source from a different
   provider; `gemini` and `kiro` return `unsupported` for anything but their
   own. `go/internal/client` lists what *exists*, not what is *allowed* — never
   infer a pairing from the registry, and never publish one in docs or marketing
   copy without reading the adapter.
2. **Proxy secrets never cross an output boundary.** Every local proxy
   authenticates each request with a per-instance secret (ADR 0006) and binds
   loopback only. `StartResult.Token` is deliberately discarded by every
   caller — the CLI, the TUI (`go/internal/tui/toggleProxyCmd`) and the desktop
   protocol all surface the endpoint only. JSON output lands in CI logs.
3. **Activation is planned, journalled, then executed.** `ActivationPlan.steps`
   is the single source of truth for both execution and rollback; the executor
   must not re-derive behavior from transport capability, or planner and executor
   can diverge silently.
4. **`writeMeta` is the atomic commit point** for an account snapshot — a failed
   backup or import can never destroy the previously saved account (ADR 0004).
5. **Every persisted mutation runs under the coordinator** with sorted, scoped
   locks (ADR 0009). Correctness must not depend on the caller locking. Stores
   own SQL; services never embed it. Account mutations lock
   `providerScope(id)` — one scope per *provider*, not per account, because all
   of them are reached through the provider's single live credential file
   (ADR 0011). Locks are re-entrant within one async context, so a service may
   lock a scope its caller already holds.
6. **Never prompt without a TTY**, never guess a source, and keep `--json`
   machine-readable on every command. Exit codes come from `anyerr.ExitCode`.
   The TUI's TTY guard must use `isatty.IsTerminal`, not
   `Mode()&os.ModeCharDevice != 0` — see the trap below.
7. **The data directory is `~/.anypick`** (`ANYPICK_HOME` overrides).

## Traps that have cost real time

- **`/dev/null` is a character device.** `Mode()&os.ModeCharDevice != 0` is not
  a TTY test: it says yes for `/dev/null`, so the TUI starts, reads no
  keystrokes and hangs under cron and CI. Use `isatty.IsTerminal(fd)`.
- **A TTY guard must run before any service is touched.** `tui.Run` checks it
  first, which is what lets a test pass a nil `*app.App` and assert the
  fail-closed path.
- **A mutation scope is not a resource identity.** `refs.ProviderScope` (lock
  scope, collapses accounts onto their provider) and `refs.Serialize` (precise
  identity, used for journal `affectedResources`) look interchangeable and are
  not. Using the precise identity for a lock silently splits one scope into many
  and reintroduces the interleaving ADR 0011 closed.
- **Error suggestions must live on the `anyerr.Error` instance**, via
  `anyerr.WithSuggestions`. Callers that format an error into a string before
  printing lose them.
- **Isolate tests.** `cmd/anypick` has `isolateEnv(t)` (temp `HOME` +
  `ANYPICK_HOME`) and `appReadyForTest`; the rest of the tree builds a root with
  `t.TempDir()` and `app.Options{SkipMigrate: true}`. Never let a test reach the
  developer's real `~/.anypick`.
- **The help tree has a golden.** `cmd/anypick/help_parity_test.go` compares the
  command tree against `go/testdata/parity/cli-tree.json`; adding or renaming a
  command fails until the fixture is regenerated.
- CI runs on `ubuntu-latest` and `windows-latest` for Go, plus macOS for the
  app. Do not claim support for platforms the release matrix does not build.
- The Codex desktop app keeps its own signed-in ChatGPT account and does **not**
  follow a switch of `~/.codex/auth.json`. AnyPick only detects the mismatch in
  order to suppress the quota readout (`go/internal/provider/codex.go`).
- **macOS chrome has one contract.** `macos/` is a System Settings
  clone (grouped `Form`, system controls, no cards). Pencil boards are IA/copy
  only. A prompt that says “native SwiftUI” is not enough if leftover tray
  cards or RGB tokens are treated as references.
- **`MenuBarExtra` is the `.window` Power Quick Panel.** Production and the
  fixture both host `macos/MenuBarMenu.swift`, matching Pencil node
  `m0s8el`. Do not replace it with a text-only menu. With no fixture variables
  set the app launches the regular macOS app and main window; the menu-bar extra
  remains a quick-access surface. Service rows must provide complete start/stop
  and restart actions; detailed configuration stays in the full app.
- **String-scan app tests lag the contract.** If a test fights
  `macos/README.md`, update the test.
- **Native app bundles are Node-free (ADR 0020).** Do not copy a private Node
  runtime, an npm/pnpm/tsx shim, an ESM plugin pack, or source-tree absolute
  paths into `AnyPick.app`. Two gates enforce it and both must reject the same
  shapes: `macos/scripts/check-node-free-app.sh` (shell, used by Xcode) and
  `go run ./cmd/anypick-artifact node-free <root>` (Go, used by CI).

## macOS SwiftUI (System Settings)

The macOS app’s main window must look like an app Apple could ship:
**System Settings structure**, not a website imitating macOS.

- **Edit `macos/`** for the app scene, panes, sheets, menu-bar content,
  and the production store/bridge. Fixture vs live is a data source, not a
  second app.
- **Use** `NavigationSplitView`, `List` (`.sidebar`), `Form` (`.grouped`),
  `Section`, `Toggle`, `Picker`, `TextField`, `Button`, native `Toolbar`,
  `Settings { }`, `ContentUnavailableView`. Platform-default fonts, spacing,
  colors, materials. Instant-apply toggles.
- **Do not use** custom cards, rounded containers, gradients, chip strips,
  handmade sidebars, RGB tokens, `TraySpacing` / `QuickPanelMetrics`, or
  pixel-perfect CSS-like padding. If SwiftUI cannot do a System Settings
  behavior, bridge AppKit (`NSAlert`, `NSOpenPanel`, `NSSavePanel`, `NSMenu`)
  instead of faking the control.
- **Settings (⌘,) is a `Settings` scene**, not a main-sidebar destination.
  Main sidebar: Overview, Apps, Accounts & APIs, Proxy Hub, Proxies,
  Activity, Account Backups.
- Preview with `pnpm macos:design`. Audit with the design-checklist skill
  routed `--surface desktop-app --artifact preferences --platform macos`.
- Full allowed/forbidden list, pane recipe, IA table, and current
  deviations: `macos/README.md`.

## Conventions

- Go is gofmt-clean and `go vet ./...` is clean. Run both before claiming a
  change is done; CI enforces them.
- Comments explain *why*, not what. Cite an ADR number when the constraint is
  load-bearing. Do not narrate the current task in a comment.
- The docs site (`docs/`) has its own formatter and lint policy; `cd docs` before
  running anything for it.
- **Do not edit `CHANGELOG.md`.** It is frozen at 1.0.0; later notes are
  generated from commits by `changelogithub`. A behavior change is recorded by
  writing a Conventional Commit subject a release reader would understand —
  `CONTRIBUTING.md` has the format.
- Provider-specific behavior belongs on the provider (`roleDefaults`,
  `suggestModels`, …). Core, CLI, and TUI must not grow a `switch (providerId)`.
