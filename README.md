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

AnyPick is one Go engine. The macOS app is Swift, the CLI and the terminal UI
are Go. There is no second runtime and no npm package: the repository ships no
TypeScript engine and no installable npm launcher (ADR 0020) — run from source
or take the GitHub Release assets.

## Run from source

Neither flow below creates a supported release artifact.

### Native macOS app

Requirements: macOS 14+, Xcode, and a Go launcher capable of selecting the
toolchain pinned in `src/go/go.mod`.

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
```

The app is one binary with two data sources: run it with no environment
variables and it talks to the Go service over `~/.anypick`; set
`ANYPICK_MACOS_FIXTURE` and it renders an in-memory fixture instead, with no
service and no disk access. Set it in the scheme (**Product → Scheme → Edit
Scheme → Run → Arguments → Environment Variables**), then Run. The full list of
fixture variables is in `src/macos/README.md`.

Users of a distributed app will not compile Swift or Go at runtime.

### CLI and terminal UI

Requirements: the Go toolchain pinned in `src/go/go.mod` (Go 1.26+).

```bash
cd src/go
go build -o /tmp/anypick ./cmd/anypick
/tmp/anypick --help
/tmp/anypick tui          # interactive terminal UI (bubbletea)
```

Or use the Makefile, which builds the same binary and pins a writable module
cache (see the note on `GOPATH` in `AGENTS.md`):

```bash
make run ARGS="--help"
make run ARGS="tui"
make sandbox ARGS="providers"   # throwaway HOME + ANYPICK_HOME
```

The terminal UI is `src/go/internal/tui`. It reads the services directly rather than
shelling out to the CLI, and it refuses to start without a TTY on stdin so a
piped invocation fails fast instead of hanging.

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

Examples:

```bash
anypick add account codex --current --name personal
anypick use codex --with codex/personal
anypick run codex

anypick add gateway openrouter-work \
  --provider openrouter \
  --endpoint https://openrouter.ai/api/v1 \
  --api-key "$OPENROUTER_API_KEY"
anypick use claude --with openrouter-work
```

Capability pairs are policy, not a Cartesian product. Read `TransportFor` in
`src/go/internal/transport` before documenting a new client/source combination.

## Development commands

```bash
make test          # go test ./... -count=1
make test-race     # the same under -race
make vet
make fmt-check     # fails if anything is not gofmt-clean
make help          # the full list
```

CI (`.github/workflows/ci.yml`) runs the same gates: format and vet and version
sync, Go tests on Ubuntu and Windows, the macOS app and helper contract, and the
docs site.

> **If you run `go` directly, set `GOPATH`.** `~/.zshrc` on this machine exports
> a `GOPATH` belonging to another project; tests that shell out to `go build`
> then stall in module resolution and appear to hang. The Makefile pins
> `GOPATH := $(HOME)/go` so `make` is always safe; otherwise use
> `GOPATH=$HOME/go go test ./...` (warm once with `go mod download all`).

The documentation site is a separate pnpm project:

```bash
cd docs && pnpm install && pnpm build
```

The local data directory is `~/.anypick`; `ANYPICK_HOME` overrides it. Account
and configuration mutations touch real developer credentials, so use an
isolated `ANYPICK_HOME` when experimenting.

## Current platform status

- The macOS 14+ SwiftUI target and bundled universal Go-helper build exist in
  source and are under active cutover testing. The first release is macOS-only.
- The CLI and the terminal UI are Go. There is no TypeScript engine and no npm
  package: distribution is source checkouts plus GitHub Release assets.
- CI exercises the Go core on Ubuntu and the Xcode app/helper contract on
  macOS. The Tauri Linux/Windows apps build from source and in CI, but their
  artifacts are staged, not published, until target-OS smoke testing lands.
  These source gates do not make an installer a released product.
- Signed installer channels beyond the macOS Release asset remain planned work.
  Do not treat their ADR target entries as available downloads.

## Documentation

- [Documentation site source](docs/src/content/docs/index.mdx)
- [Contributing](CONTRIBUTING.md)
- [Architecture decisions](adr/README.md)
- [Security policy](SECURITY.md)
- [Design checklist skill](.claude/skills/design-checklist/README.md)

AnyPick is licensed under the [MIT License](LICENSE).
