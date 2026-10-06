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
> Releases are produced by the signed GitHub Actions workflow. A public release
> is only download-ready after the required signing/notarization and target-OS
> smoke gates pass; package-manager channels are opt-in deployment targets. See
> [Installing AnyPick](#installing-anypick) and the release workflow for the
> exact channels configured for this repository.

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
are Go. There is no second runtime: the npm package is only a thin launcher
that downloads the matching checksum-verified Go CLI (ADR 0020).

## Installing AnyPick

The GitHub Release is the canonical download source. It contains the signed
macOS DMG/app archive, the Tauri Linux/Windows installers, and checksum-verified
CLI archives for all supported CPU targets. Every desktop payload is
smoke-tested on its own OS before staging: the Linux deb/AppImage/rpm and the
Windows MSI are extracted and the bundled Go helper must answer with the planned
version. The GUI is not launched on hosted runners. The workflow also emits
WinGet/Scoop metadata (submission to `winget-pkgs` is manual) and can update a
configured Homebrew tap.

```bash
# Native macOS app, Linux/Windows installers, and CLI archives:
# https://github.com/outof0/anypick/releases/latest

# Thin Node launcher (Node 18+). On first run it downloads the matching
# native CLI archive from GitHub Releases into ~/.cache/anypick.
# Breaking change from 1.1.0: the npm package is this launcher for the native
# CLI. It no longer runs third-party plugins (ADR 0021). Pin anypick@1.0.0 if
# you still need the old JavaScript package.
npm install --global anypick
```

When the external taps are configured and the corresponding publish switch is
enabled, the package-manager commands are:

```bash
# Homebrew — coming soon (the outof0/homebrew-tap repository is not public yet)
# brew tap outof0/tap
# brew install outof0/tap/anypick
# brew install --cask outof0/tap/anypick

winget install outof0.AnyPick   # after the manifest is accepted into winget-pkgs
scoop bucket add outof0 https://github.com/outof0/scoop-bucket
scoop install anypick
```

The npm launcher and native bundles do not share a Node service. Desktop
artifacts remain Node-free; npm is a distribution adapter only.

### Uninstall and rollback

- **npm CLI**: `npm uninstall --global anypick`, then delete the downloaded
  binary cache (`~/.cache/anypick` on macOS/Linux,
  `%LOCALAPPDATA%\\AnyPick` on Windows).
- **Homebrew**: `brew uninstall anypick` (or `brew uninstall --cask anypick`).
- **macOS app**: quit AnyPick and move `AnyPick.app` to the Trash. Local data
  stays in `~/.anypick`; delete that directory only if you also mean to remove
  every saved account, proxy state, and activity entry.
- **Windows/Linux installers**: uninstall through Settings → Apps (MSI/NSIS) or
  your distribution's package manager.
- **Rollback**: install the previous release from the GitHub Releases page. The
  app and CLI are self-contained; `~/.anypick` data is forward-compatible, and
  account backups (readable JSON) are the supported way to move credentials.

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

> **Prefer `make` over a bare `go`.** The Makefile pins `GOPATH` to
> `$HOME/go` and finds the toolchain. A shell that exports `GOPATH` at
> another project's module cache makes tests that shell out to `go build`
> stall in module resolution. If you invoke `go` directly:
> `GOPATH=$HOME/go go test ./...` (warm once with `go mod download all`).

The documentation site is a separate pnpm project:

```bash
cd docs && pnpm install && pnpm build
```

The local data directory is `~/.anypick`; `ANYPICK_HOME` overrides it. Account
and configuration mutations touch real developer credentials, so use an
isolated `ANYPICK_HOME` when experimenting.

The loopback `anypick serve` status API also exposes `GET /agents`: a
credential-free worker manifest for local coordinators. It reports installed
clients, the saved route, provider-owned transport capability, selected model,
and proxy health. Credentials, gateway URLs, proxy tokens, and filesystem
paths never cross that boundary. `anypick doctor --json` includes the same
redacted proxy-environment diagnostics, including mixed-case environment
conflicts that can make a spawned worker take a different network path.

## Current platform status

- The macOS 14+ SwiftUI target and bundled universal Go-helper build exist in
  source and are signed/notarized in the macOS release job.
- The Tauri Linux/Windows apps build installers in the default all-platform
  release path. Before staging, `packaging/smoke/` extracts each Linux
  deb/AppImage/rpm and the Windows MSI and runs the bundled Go helper
  (`--version` + `providers`) in a throwaway HOME. The GUI is not launched.
- The CLI release job builds macOS Intel/Apple Silicon, Linux x64/arm64, and
  Windows x64/arm64 archives. `npm/anypick` selects only those archives.
- Homebrew, WinGet and Scoop are external repositories. Their generated
  manifests are attached to each release; publishing them requires the
  corresponding repository secret/variable and is deliberately opt-in. WinGet
  additionally requires a submission to `microsoft/winget-pkgs`.

## Documentation

- [Distribution and release operations](packaging/README.md)
- [Documentation site source](docs/src/content/docs/index.mdx)
- [Contributing](CONTRIBUTING.md)
- [Architecture decisions](adr/README.md)
- [Security policy](SECURITY.md)

AnyPick is licensed under the [MIT License](LICENSE).
