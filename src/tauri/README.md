# AnyPick desktop app (Linux and Windows)

This is one of two native front ends. The other is the macOS app in
`src/macos/` (SwiftUI); this one is Tauri 2 + React and ships on Linux and
Windows (ADR 0020).

**This app owns no AnyPick state and never reads credentials.** It launches,
handshakes with, supervises and shuts down the bundled Go service, then renders
what that service sends it (ADR 0019 §1):

- app → service: `invoke`, `mutate`, `logs`, and simple commands;
- service → app: filtered `snapshot`, `result`, and `logs` messages.

The transport is a hidden `anypick desktop-service --stdio` process whose stdout
is protocol-only, behind a version/capability handshake that gates every
mutation (ADR 0019 §3). There is no permanent daemon, no localhost HTTP API, no
socket, and no gRPC — any of those needs a new accepted ADR.

## What is bundled

- **The Go service**, as a Tauri `externalBin` sidecar:
  `binaries/anypick-core` in `src-tauri/tauri.conf.json`, resolved per target by
  `src-tauri/build.rs`. If you move a directory under `src/`, run `make paths`
  first: that build script is one of the paths it checks.
- **No Node runtime.** npm and Vite build the UI bundle at build time; the
  shipped artifact contains no Node, npm, pnpm or ESM shim (ADR 0020 §8). Two
  gates enforce it — `src/macos/scripts/check-node-free-app.sh` and
  `go run ./cmd/anypick-artifact node-free <root>` — and both must reject the
  same shapes.

## UI

React 19 + Vite + Tailwind v4 under `ui/`, built into `frontend/` (which Tauri
consumes as `frontendDist`). Do not edit files under `frontend/` by hand.
Shared design tokens and component styles live in `ui/styles.css`
(`@import "tailwindcss"` plus the app look).

## Commands

From the repository root:

| Command | Purpose |
| --- | --- |
| `make tauri-deps` | Install the UI dependencies |
| `make tauri-check` | Typecheck the UI without emitting output |
| `make tauri-ui` | Build the UI bundle into `src/tauri/frontend` |
| `make tauri-build` | Build the packaged desktop app for this platform |
| `make tauri-dev` | Run the desktop app against the real Go service |

Build on the target desktop OS after installing the Tauri platform
prerequisites (on Linux: `libwebkit2gtk-4.1-dev`,
`libayatana-appindicator3-dev`, `librsvg2-dev`, `patchelf`).

## The desktop protocol version is declared three times

Go (`internal/buildinfo`), Rust (`src-tauri/src/protocol.rs`) and Swift
(`src/macos/AppServiceClient.swift`) each declare it independently, and nothing
links them. `make protocol-sync` asserts all three agree — run it whenever you
bump the protocol, because bumping it on one platform only produces a failure
that is otherwise very hard to trace.

## CI

The `tauri` job in `.github/workflows/ci.yml` (ubuntu) runs on every push and
PR to `main`: `make paths protocol-sync`, `npm ci`, `make tauri-check`,
`make tauri-ui`, then `cargo check --locked`.

Out of scope for CI (needs a real desktop and installed clients): running
against a developer's `~/.anypick`, tray-icon click geometry, and OS
notification surfaces. Use `make sandbox ARGS="status"` when you need to poke
at real state without touching your own data root.
