# AnyPick desktop app (Linux and Windows)

This is one of two native front ends. The other is the macOS app in
`src/macos/` (SwiftUI); this one is Tauri 2 + React and targets Linux and
Windows (ADR 0020). The release workflow defaults to the all-platform scope:
these apps build AppImage/deb/rpm, MSI and NSIS artifacts, and each payload is
smoke-tested on its own OS (`packaging/smoke/`) before it can reach a GitHub
Release. `macos-only` remains available for a narrowed release. The GUI itself
is not launched on a hosted runner; the smoke gate extracts the installer and
runs the bundled Go helper.

**This app owns no AnyPick state and never reads credentials.** It launches,
handshakes with, supervises and shuts down the bundled Go service, then renders
what that service sends it (ADR 0019 §1):

- app → service: `invoke`, `mutate`, `logs`, and simple commands;
- service → app: filtered `snapshot`, `result`, and `logs` messages.

The transport is a hidden `anypick desktop-service --stdio` process whose stdout
is protocol-only, behind a version/capability handshake that check every
mutation (ADR 0019 §3). There is no permanent daemon, no localhost HTTP API, no
socket, and no gRPC — any of those needs a new accepted ADR.

## What is bundled

- **The Go service**, as a Tauri `externalBin` sidecar:
  `binaries/anypick-core` in `app/tauri.conf.json`, resolved per target by
  `app/build.rs`. If you move a directory under `src/`, run `make check-paths`
  first: that build script is one of the paths it checks.
- **No Node runtime.** npm and Vite build the UI bundle at build time; the
  shipped artifact contains no Node, npm, pnpm or ESM shim (ADR 0020 §8). Two
  check enforce it — `src/macos/scripts/check-node-free-app.sh` and
  `go run ./cmd/anypick-artifact node-free <root>` — and both must reject the
  same shapes.

## UI

React 19 + Vite + Tailwind v4 under `ui/`, built into `frontend/` (which Tauri
consumes as `frontendDist`). Do not edit files under `frontend/` by hand.
There is exactly one stylesheet, `ui/styles.css`: an `@import "tailwindcss"`, an
`@theme` block holding the System Settings tokens, and a `@layer base` block for
the handful of element defaults the chrome needs. A pane composes
`ui/primitives.tsx` and never styles a row itself.

**Do not add a second stylesheet.** There were two (`styles.css` and
`ui/system-settings.css`) and they collided over seven class names while leaving
three classes the UI emitted undefined. `styles.css` loaded last, so it won
every equal-specificity tie and the System Settings metrics lost. Utilities
cannot do either: there is nothing to collide with and nothing to forget to
define.

### Layout

```text
lib/contract.ts   the TypeScript mirror of go/internal/desktop/snapshot.go
lib/wire.ts       the stdio framing: KIND\tbase64(JSON), both directions
lib/service.ts    the only place a pane asks the engine to do something
lib/model.ts      display and derivation rules ported from src/macos/
lib/bridge.ts     the Tauri boundary, injectable for tests
panes/            one file per destination, composing ui/primitives.tsx
shell/            Sidebar, Toolbar, NoticeBanner, Symbol
ui/               primitives.tsx (the System Settings building blocks) and Dialog.tsx
```

`lib/contract.ts` is checked against the Go struct by
`test/contract-drift.test.ts`, which reads `snapshot.go` and fails the build
when a field exists there and not in the mirror. A contract that drifts from
the engine is worse than none, because the failure is a blank cell with nothing
to grep for.

`lib/model.ts` holds the copy rules. They are ported from
`src/macos/DesignDisplay.swift` and `AppIcon.swift`, and the expectations came
from the macOS XCTest suite first. That is deliberate: the macOS app's copy is
the product's copy, so a Linux user reading a Claude proxy row should read the
same words a macOS user does. A rule that lives in a view instead belongs here,
where it can be tested without a webview.

### What "matches the macOS app" means here

Structural, metric, and state parity: the same destinations in the same order,
the same rows with the same labels, the same states, the same flows, and the
same metrics in `ui/system-settings.css` with a named Swift counterpart beside
each one.

It is **not** a pixel-exact image diff, and this port will not claim one. macOS
is SwiftUI on AppKit; this is a webview (WebKitGTK on Linux, WebView2 on
Windows). Different rasterizers, font engines, text metrics, and window chrome.
A zero-pixel diff between them is not achievable, so a claim of one would be
false.

Parity is verified two ways, both in CI. `make test-tauri-ui` asserts behavior
against the fixture. `make check-parity` reads every macOS
`accessibilityIdentifier` and checks it against `test/parity-map.ts`, which is
the definition of "every feature ported" as a machine check rather than a
claim. `make screenshots-tauri-ui` renders each destination and dialog to
`.screenshots/` for a human to look at; it needs Playwright, so it is not a
gate.

`make check-parity` deliberately checks that a control *exists*, not how it
behaves, so state parity has its own gates in
`test/command-state.test.tsx`.

### An icon command owns its state

macOS reaches for an `NSButton` in `DesignIconButton` so AppKit paints the
hover, the press and the disabled bezel. A web button already paints all three
from CSS, so the control kind was never the parity gap. The state was: eight
copy commands reported nothing at all, and one swapped its own glyph inline.

`IconButton` therefore carries a `phase` of `idle`, `working` or `confirmed`, and
`CopyButton` makes a copy a component rather than a prop, so a pane cannot grow
a second spelling. Both draw through `Symbol`, which is the only place a path
lives.

- A copy reports whether the text reached the clipboard. `service.copy` returns
  a boolean for this: a control that confirmed a refused write would be lying to
  the one user who cannot select the value and read it back.
- A `working` command sets `aria-busy` and is disabled, so a second click cannot
  land on the same single-flight slot.
- A row-menu command is the one place that may call `service.copy` directly, and
  the exemption is a named list in the test rather than a comment.

**A refresh greys; it does not spin.** The engine single-flights a mutation, so
a refresh disables while its own request is in flight and that disabled state is
the feedback. Spinning it would need a per-command in-flight slot with its own
confirm path, for a state the HIG reserves spinners for background work. macOS
made the same call; see `macos/README.md`.

Three regressions here are invisible to a rendered test, because jsdom never
loads `styles.css` and never fires a hover. `test/command-state.test.tsx` reads
the source for all three, the same way `test/colour-tokens.test.ts` does:
a `.spinner` that never named its `@keyframes`, a copy glyph drawn by hand, and
an icon-only control with no name.

### The parity map is the work list

`test/parity-map.ts` has one row per macOS feature: `done` names the Tauri
element that carries it, `todo` means macOS has it and Tauri does not, and
`skip` requires a written reason. `test/parity.test.ts` fails when a macOS
feature has no row, when a `done` row names an element the UI does not emit, or
when a row names a macOS feature that no longer exists. The map cannot go green
by accident, which is the property the check exists to provide.

`make check-parity` prints the `todo` set. The port is complete when it is
empty.

### Tests

`make test-tauri-ui` runs vitest in jsdom. It needs no webview, no Go service,
and no display, so it runs in CI on the same runner as the Go tests. The panes
are rendered against `test/fixture.ts`, a snapshot shaped exactly as
`snapshot.go` serialises, and asserted on through `test/harness.tsx`'s
`FakeBridge`, which records the commands a pane sent.

`createService` is where a request id is minted and where a control is
released: on the engine's terminal frame, matched by id, never on a timer. The
tests assert that directly, because a timeout-based release re-enables a button
whose mutation has not landed.

Adding a command to a pane means adding it to `lib/service.ts` first. A pane
never builds a wire line: the engine rejects any key that is not on the
operation's allowlist with a `MALFORMED_FRAME` that names nothing useful.

## Commands

From the repository root:

| Command | Purpose |
| --- | --- |
| `make install-tauri` | Install the UI dependencies |
| `make typecheck-tauri-ui` | Typecheck the UI without emitting output |
| `make test-tauri-ui` | Run the desktop UI tests (jsdom, no webview needed) |
| `make check-tauri-ui` | Typecheck and test the desktop UI |
| `make check-parity` | Report which macOS features have no Tauri counterpart |
| `make screenshots-tauri-ui` | Screenshot every pane and dialog (needs Playwright) |
| `make build-tauri-ui` | Build the UI bundle into `src/tauri/frontend` |
| `make build-tauri` | Build the packaged desktop app for this platform |
| `make run-tauri` | Run the desktop app against the real Go service |

Build on the target desktop OS after installing the Tauri platform
prerequisites (on Linux: `libwebkit2gtk-4.1-dev`,
`libayatana-appindicator3-dev`, `librsvg2-dev`, `patchelf`).

## The desktop protocol version is declared four times

Go (`internal/buildinfo`), Rust (`src/tauri/app/src/protocol.rs`), Swift
(`src/macos/AppServiceClient.swift`) and the webview
(`src/tauri/ui/lib/wire.ts`) each declare it independently, and nothing links
them. `make check-protocol` asserts all four agree — run it whenever you bump
the protocol, because bumping it on three and missing the fourth produces a
failure that is otherwise very hard to trace: the webview drops every frame
that declares `protocolVersion`, so the window stays empty while the other
front ends look healthy.

## CI

The `tauri` job in `.github/workflows/ci.yml` (ubuntu) runs on every push and
PR to `main`: `make check-paths check-protocol`, `npm ci`, `make typecheck-tauri-ui`,
`make test-tauri-ui`, `make build-tauri-ui`, then `cargo check --locked`.

The UI tests run on the headless Linux runner because they need jsdom and
nothing else: no webview, no Go service, no display. That is why they are
enforced here and not left to a developer's machine.

Out of scope for CI (needs a real desktop and installed clients): running
against a developer's `~/.anypick`, tray-icon click geometry, and OS
notification surfaces. Use `make sandbox ARGS="status"` when you need to poke
at real state without touching your own data root.
