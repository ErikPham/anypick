# 0018. The macOS product is one Dock app and one Swift tree

- Status: accepted
- Date: 2026-08-15

## Context

AnyPick on macOS had become three launchers and two Swift trees. `pnpm dev`
compiled leftover tray chrome (`NativeMainWindow`, `NativeQuickPanel`) together
with the System Settings UI in `src/tray/macos/`, then hosted only the latter.
`pnpm macos:design` compiled the Settings UI alone behind
`#if ANYPICK_DESIGN_FIXTURE`. `pnpm macos:app` compiled both trees and fed a
Tauri demo snapshot. The product is a Dock app with a menu-bar extra, not a
tray-only utility. Two `Models.swift` files forced a `Design*` compile prefix.

## Decision

1. All Swift for the macOS app lives in `macos/`. `src/tray/` keeps the
   Node supervisor, snapshot protocol, and compile/launch scripts.
2. There is one `@main` (`AnyPickApp`). Fixture vs live is a data source
   (`MacOSDesignModel.fromEnvironment()` vs `ProductionDesignModel`), selected
   by `ANYPICK_MACOS_FIXTURE` / `ANYPICK_MACOS_DESIGN`.
3. `pnpm dev` and `pnpm macos:design` compile the same sources. Leftover
   `Native*` views are deleted, not hosted.
4. The menu-bar extra remains `MenuBarExtra` `.window` hosting `MenuBarMenu`.
   Closing the main window does not stop proxies.

## Consequences

- UI work happens in `macos/`. Do not recreate panes under `src/tray/`.
- Compile no longer prefixes files `Design*`.
- Cached `AnyPick.app` fingerprints as `bundle:AnyPick.app/v3`.
- `anypick tray run` stays as the hidden supervisor entry that launches this
  app. User-facing language should say “app”, not “tray app”.
