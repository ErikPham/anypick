# 0020. Native distributions are Node-free full desktop applications

- Status: accepted
- Date: 2026-08-26
- Supersedes: [ADR 0019](0019-native-app-cutover-architecture.md)

## Context

ADR 0019 correctly moved desktop lifecycle ownership into the SwiftUI and
Tauri applications and selected a bundled Go service over bounded, versioned
stdio. It also retained two migration exceptions: a private Node runtime for
ESM plugins and a TypeScript Ink TUI in the npm channel.

Those exceptions pull the product back toward two runtime stacks. They make a
downloaded application larger and harder to sign, audit, recover, and test on a
clean machine. They also encourage work on JavaScript plugin parity before the
main application can be archived, installed, and used without developer tools.

The product requirement is now stricter:

- a downloaded AnyPick application is a complete desktop application, not a
  tray utility;
- it works without Node.js or any developer toolchain installed;
- npm remains only an optional, convenient way to install and launch the same
  Go implementation.

## Decision

1. **The desktop product is a full application.** macOS ships a regular SwiftUI
   Dock application with a main window and a separate `Settings` scene.
   Windows and Linux ship a regular Tauri application with a visible main
   window and taskbar/dock presence. `MenuBarExtra` and status-area/tray items
   are auxiliary quick-control surfaces. They never own lifecycle, replace the
   main application, or become the only supported interface.

2. **The application owns one bundled Go engine.** The native shell launches,
   handshakes with, supervises, and shuts down a bundle-relative
   `anypick-core desktop-service --stdio` child. Accounts, bindings,
   activation, proxies, pools, Hub, backups, settings policy, diagnostics, and
   desktop commands remain in Go behind `internal/app`. Swift and Rust perform
   presentation and typed host effects only.

3. **Native release artifacts are Node-runtime-free.** A DMG, app bundle, NSIS
   installer, AppImage, `.deb`, or `.rpm` must not contain or execute Node,
   `plugin-host.mjs`, npm, pnpm, tsx, or another server-side JavaScript/plugin
   runtime. It must not consult `PATH` for Node, Go, Rust, Swift, Xcode, or build
   tools. Static frontend assets rendered by Tauri's OS webview are presentation,
   not a Node exception, and may not own domain behavior. Build and test tooling
   in the source repository may use Node; that tooling is not a product runtime
   and must not leak into an installed artifact.

4. **npm is a distribution adapter, not a second application stack.**
   `npm install anypick` may use the minimum JavaScript glue required by npm to
   select and launch the correct platform package. Supported commands then run
   the same Go binary shipped through native and CLI channels. The launcher
   preserves arguments, stdio, signals, and exit status, but owns no domain
   behavior. A TypeScript service fallback, Ink-only product behavior, or a
   Node plugin runtime is not part of the target architecture. Existing public
   JavaScript APIs and the Ink TUI must either move to Go-backed equivalents or
   be retired through an explicit semver-major migration.

5. **Executable ESM plugins are not a native-release compatibility
   requirement.** First-party providers and clients compile into the Go core.
   Compatible-provider extensions use declarative, data-only definitions whose
   validation and behavior remain core-owned as established by ADR 0015.
   Existing Node plugin-host code is migration scaffolding and test provenance,
   not a shippable dependency. A future need for executable third-party
   extensions requires a new ADR defining a Node-free sandbox, capability
   model, resource bounds, signing, and recovery contract; WebAssembly is the
   preferred direction, not an implicit Node exception.

6. **Desktop IPC remains bounded, versioned stdio.** A capability/version
   handshake precedes snapshots and mutations. There is no first-release daemon,
   localhost control API, Unix socket, named pipe, gRPC service, or second
   serialization stack. Closing the main window does not stop services;
   explicit Quit performs bounded graceful shutdown. Any change to this model
   requires another accepted ADR.

7. **Platform effects stay narrow and typed.** Launch at login, file panels,
   browser/System Settings opening, notifications, Dock/taskbar/window/menu
   behavior, and installer/updater integration are host-effect requests. The
   Swift/Rust host never resolves accounts, chooses transports, builds an
   activation plan, or mutates AnyPick persistence independently.

8. **Release evidence is artifact-based.** Native readiness requires an
   artifact scan proving the installed tree is Node-free, plus an installed-app
   smoke under a restricted `PATH` on a clean account. macOS additionally
   requires universal architecture verification, nested-helper signing,
   notarization, stapling, and lifecycle/UI automation. Compilation from the
   source tree is not evidence that a downloadable application is ready.

9. **The cutover is app-first.** Finish the macOS application and its signed,
   notarized, clean-machine installation path before expanding optional plugin
   compatibility or declaring Windows/Linux support. Windows/Linux then follow
   the same full-app and Node-free gates. npm packaging follows the same Go
   binaries and must not create feature or service-engine divergence.

## Consequences

- ADR 0019's private signed Node plugin-host exception and retained Ink TUI are
  withdrawn. Its app-owned lifecycle, single Go engine, bounded stdio, no-daemon
  rule, host-effect boundary, and initial supported platform matrix are carried
  forward here.
- macOS and Tauri bundling must remove `plugin-host.mjs`, private Node lookup,
  and the environment variables that activate the Node plugin host.
- Remaining catalog-policy and pool-lifecycle work that exists only to complete
  arbitrary JavaScript plugin parity is no longer a native-release blocker.
- First-party functionality must be available from Go before a native artifact
  can ship. An installed ESM plugin may require migration to a declarative
  definition or may become unsupported in the semver-major cutover.
- The npm package can remain familiar to users, but it is only a thin path to
  the platform Go binary. It cannot justify keeping the Node service, TUI, or
  plugin runtime alive indefinitely.
- Release automation fails closed if an artifact contains forbidden runtimes,
  developer paths, or toolchain assumptions. A locally compiled `.app` is never
  described as download-ready without signing, notarization, installation, and
  clean-account evidence.
