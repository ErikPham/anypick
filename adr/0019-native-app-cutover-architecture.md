# 0019. The native app owns lifecycle over a bundled Go service on bounded stdio

- Status: superseded by [ADR 0020](0020-node-free-native-app-distribution.md)
- Date: 2026-06-18

## Context

AnyPick is moving from a Node-owned desktop architecture to native
applications (SwiftUI on macOS, Tauri on Windows/Linux) over one Go engine.
Today the ownership is inverted in three places:

- the Node supervisor compiles Swift at runtime (`src/tray/supervisor-native.ts`)
  and spawns/owns the app;
- the Go `app run` command spawns the native app through `ANYPICK_APP_BIN` and
  pipes it (`go/cmd/anypick/main.go`) — Go supervising the shell that should
  supervise it;
- the Tauri process reads its own stdin and writes commands to stdout for a
  Node parent (`src/tray/tauri/src-tauri/src/main.rs`), so the web view is a
  protocol peer of its own UI host.

A production installer cannot require Node, Go, Rust, Swift, or Xcode on the
user's machine, so none of these inversions can survive the cutover.

## Decision

1. **App-owned lifecycle.** The SwiftUI/Tauri application launches, handshakes
   with, supervises, and shuts down the bundled AnyPick Go service. The
   menu-bar/status-area item is an auxiliary surface of the application, not
   its owner or parent. Closing a window never quits the app or stops services;
   explicit Quit does, after a graceful-shutdown timeout.

2. **One engine, one composition root.** Domain logic (accounts, bindings,
   activation, proxies, pools, Hub, backups, settings policy, diagnostics,
   desktop command execution) lives in Go services behind
   `internal/app`. Shells and handlers parse/validate transport input and call
   services; they never re-implement behavior. The same services back the
   native CLI, the desktop service, hidden proxy/pool/Hub children, and npm's
   platform-binary execution.

3. **Bounded, versioned stdio IPC.** The desktop transport is a hidden
   `anypick desktop-service --stdio` command whose stdout is protocol-only.
   A version/capability handshake precedes the first snapshot and gates every
   mutation. No permanent daemon, localhost HTTP API, Unix socket, named pipe,
   gRPC, or new serialization framework in the first release; any of those
   requires a new accepted ADR and a demonstrated need. The existing tray
   protocol DTOs are reused and completed, not gratuitously rewritten.

4. **No first-release OS daemon.** Services live only while the app (or an
   explicitly spawned CLI child) lives. If surviving an explicit Quit becomes a
   product requirement, write a new ADR first.

5. **Narrow host-effect boundary.** Operations that need the platform (launch at
   login, file panels, browser/System Settings opening, notifications, Dock/
   taskbar/window/menu behavior, installer/updater integration) are typed
   host-effect requests the Go service sends and awaits typed results for. The
   Swift/Rust host executes only the platform operation — it never resolves
   accounts, picks transports, shapes activation, or mutates AnyPick
   persistence independently.

6. **Private signed Node plugin-host exception.** Exact compatibility with
   released ESM plugins requires Node, so native bundles may ship a private,
   signed Node plugin-host runtime. It starts lazily only when JavaScript
   plugins are enabled, resolves only by trusted bundle-relative path (never
   from `PATH`), verifies plugin digests before importing code, and is never
   the application service.

7. **npm remains a convenience channel.** npm keeps the TypeScript Ink TUI
   (`anypick tui`) and public JS APIs; non-TUI commands execute the platform Go
   binary through a small launcher. Two production service engines are not
   maintained.

8. **Initial matrix.** macOS 14+ (universal arm64+x86_64), Windows 10/11 x64,
   Linux x64 against a documented minimum distribution baseline. Arm64
   Windows/Linux wait for genuinely tested hardware. Distribution targets:
   GitHub Releases, signed/notarized DMG, Homebrew Cask + Formula, signed NSIS
   + WinGet, AppImage/.deb/.rpm, npm + platform binary packages.

## Consequences

- Bundled helpers resolve by bundle-relative path and must match expected
  version/digest before use; `PATH` is never consulted.
- Protocol/app/engine version mismatches produce a useful degraded screen, not
  undefined behavior; unexpected service exits restart with bounded backoff and
  a maximum restart count.
- Secrets stay in Go: snapshots and frames never carry raw proxy tokens or saved
  credentials (ADR 0006 boundaries still apply).
- Release automation must sign nested helpers before the outer bundle and pass
  notarization before a macOS release ships; unsigned artifacts are never
  presented as production-ready.
- The npm channel keeps working during and after cutover; the Node service
  fallback is removed only after the native launcher is declared stable (beta
  window documented).
