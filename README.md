<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img alt="AnyPick" src="assets/logo.svg" width="380">
  </picture>
</p>

<p align="center"><b>Pick any. Code on.</b></p>

AnyPick lets you choose which saved AI account or compatible API source powers
each supported coding client. It manages account snapshots, client bindings,
local protocol translators, pools, and Proxy Hub from one native application or
from the terminal.

> [!IMPORTANT]
> This repository is a development preview. It does not currently publish a
> signed/notarized macOS app, DMG, Homebrew package, Windows/Linux installer, or
> another production-ready desktop artifact. The platform and distribution
> matrix in [ADR 0020](adr/0020-node-free-native-app-distribution.md) is a target,
> not a statement of current release support.

## Architecture

The production desktop direction is app-first:

```text
AnyPick.app (SwiftUI)
├── main System Settings-style window
├── menu-bar quick panel
└── Contents/Helpers/anypick-core
    └── anypick desktop-service --stdio
```

The native app is the lifecycle owner. It launches the bundled Go helper by a
bundle-relative path, performs a version/capability handshake, receives bounded
protocol frames, supervises bounded restarts, and stops the child on explicit
Quit. Closing the main window does not quit the app. The menu-bar item is a
compact surface of the same app, not a separate tray utility and not its parent.

The helper contains the desktop service and domain behavior. Raw credentials
and per-instance proxy secrets stay behind the Go boundary. The desktop
transport is private, versioned stdio; it is not a public localhost API or a
permanent daemon.

See [ADR 0020](adr/0020-node-free-native-app-distribution.md) for the accepted
cutover decision and [the macOS source contract](src/macos/README.md) for the UI
and lifecycle implementation.

## Run from source

There are two current developer flows. Neither creates a supported release
artifact.

### Native macOS app

Requirements: macOS 14+, Xcode, and a Go launcher capable of selecting the
toolchain pinned in `go/go.mod`.

```bash
open src/macos/AnyPick.xcodeproj
```

Select the shared **AnyPick** scheme and Run. The Xcode build phase compiles the
pinned Go service for the target architecture, places it at
`AnyPick.app/Contents/Helpers/anypick-core`, and signs the nested helper before
the outer app when signing is enabled. The project references the Swift files in
`src/macos/` directly; it does not keep a second production source tree.

Useful checks:

```bash
xcodebuild -project src/macos/AnyPick.xcodeproj -scheme AnyPick \
  -destination platform=macOS CODE_SIGNING_ALLOWED=NO test
pnpm macos:design                  # in-memory UI fixture, not production launch
pnpm macos:design:check
```

`pnpm macos:design` uses `swiftc` only as a repository fixture/build check.
Users of a future distributed app will not compile Swift or Go at runtime.

### CLI and terminal UI

Requirements: Node.js 22.5+ and pnpm.

```bash
pnpm install
pnpm dev --help
pnpm dev tui
```

`pnpm dev …` runs the TypeScript source through `tsx`; it is the current
repository workflow for the CLI/TUI and compatibility commands. It is not the
shipping desktop topology and should not be used as evidence that end users
need Node in the native app.

## Mental model

| Word | Meaning |
| --- | --- |
| **Client** | The coding tool being configured, such as Claude Code or Codex CLI |
| **Source** | A saved account, API gateway, proxy pool, Proxy Hub, or preset |
| **Binding** | A concrete client → source selection, including model roles |
| **Preset** | A named binding template saved explicitly with `--save` |

```text
use   = set the default binding
run   = launch with an effective binding
link  = set a project-local binding
```

Examples from the source CLI:

```bash
pnpm dev add account codex --current --name personal
pnpm dev use codex --with codex/personal
pnpm dev run codex

pnpm dev add gateway openrouter-work \
  --provider openrouter \
  --endpoint https://openrouter.ai/api/v1 \
  --api-key "$OPENROUTER_API_KEY"
pnpm dev use claude --with openrouter-work
```

Capability pairs are policy, not a Cartesian product. Read `transportFor` in
`src/sources/account-adapters.ts` before documenting a new client/source
combination.

## Development commands

```bash
pnpm check                         # format, lint, types, release checks, tests
pnpm test
pnpm vitest run tests/<file>.test.ts
pnpm go:test                       # Go race suite
cd docs && pnpm build              # documentation site (separate project)
```

The local data directory is `~/.anypick`; `ANYPICK_HOME` overrides it. Account
and configuration mutations touch real developer credentials, so use an
isolated `ANYPICK_HOME` when experimenting.

## Current platform status

- The macOS 14+ SwiftUI target and bundled universal Go-helper build exist in
  source and are under active cutover testing.
- The TypeScript CLI/TUI remains a source-development and compatibility path.
- CI exercises TypeScript and Go on Ubuntu, the Go core on a real Windows
  runner, and the Xcode app/helper contract on macOS. These source gates do not
  make an installer a released product.
- Native Windows/Linux applications and all signed installer channels remain
  planned work. Do not treat their ADR target entries as available downloads.

## Documentation

- [Documentation site source](docs/src/content/docs/index.mdx)
- [Contributing](CONTRIBUTING.md)
- [Architecture decisions](adr/README.md)
- [Security policy](SECURITY.md)
- [Design checklist skill](.claude/skills/design-checklist/README.md)

AnyPick is licensed under the [MIT License](LICENSE).
