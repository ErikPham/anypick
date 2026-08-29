# macOS System Settings contract

This directory is the **only Swift tree** for the macOS app: scenes, panes,
sheets, menu-bar content, in-memory fixture, `TrayStore`, and
`ProductionDesignModel`. `pnpm dev` and `pnpm macos:design` compile the same
sources. Fixture vs live is a data source (`ANYPICK_MACOS_FIXTURE`), not a
second app.

The product is a full macOS Dock app whose **main window must look and behave
like a first-party macOS System Settings window**, not a website, not a
dashboard, and not a custom design-tool mock. A menu-bar extra remains
available as a compact power surface; it does not own the application
lifecycle. Shipped `.app` trees are Node-free (ADR 0020): no `plugin-host.mjs`,
private Node, npm/pnpm/tsx, or leaked developer paths.

If a change would look out of place next to **System Settings → Displays**
or **System Settings → Battery**, it is the wrong change.

## Authority (highest wins)

1. This file and the macOS section in repo-root `AGENTS.md`.
2. Apple HIG for macOS — Settings, Windows, Sidebars, Menus, Alerts,
   Accessibility: <https://developer.apple.com/design/human-interface-guidelines/designing-for-macos/>
3. The shipped System Settings app on the machine you are designing on.
   Open it. Match spacing, control kinds, and pane rhythm. Do not guess.
4. Product vocabulary and pane/sheet **content** in `design/design.pen`.
   Pen is not 100% native (Inter, Phosphor, drawn chrome) — ship SF Pro,
   SF Symbols, and system controls.
5. `tests/macos-design-ui.test.ts` only for **isolation / credentials /
   native-control presence**. String locks on pixel widths or Pencil
   measurements are stale — update the test.

**Not authority.** Do not copy these into new UI:

| Source | Why it misleads |
| --- | --- |
| Leftover tray cards / RGB tokens / chip rows | Superseded chrome. Do not reintroduce. |
| Pen stand-in metrics (Inter, Phosphor, 54×24 switch, drawn traffic lights) | Mock fidelity only — not Swift constants |
| `src/tray/tauri/ui/` | Web tray for Linux; not a macOS reference |
| iOS Settings / inset-grouped “card” screenshots | Different platform |
| A generic “make it native” prompt without this file | Agents still absorb the files above |

## One tree

Edit this directory for chrome, panes, sheets, menu-bar content, store, and
the production bridge. `src/tray/` is Node only (supervisor + protocol).

| Command | Data |
| --- | --- |
| `pnpm dev` | Live `~/.anypick` through `ProductionDesignModel` |
| `pnpm macos:design` | In-memory fixture. Same Swift. |

Preview the fixture, not a screenshot of a previous session:

```bash
pnpm macos:design                    # populated Overview + tray Ready
pnpm macos:design --empty            # first-run Get Started + tray First Run
pnpm macos:design --flow tray        # board 29 Ready
pnpm macos:design --flow tray-attention
pnpm macos:design --flow tray-use-with
pnpm macos:design --flow tray-switching
pnpm macos:design --flow tray-recovery
pnpm macos:design --flow tray-first-run
pnpm macos:design --flow getting-started
pnpm macos:design --destination proxies     # Hub + per-account proxies
pnpm macos:design --destination apps
pnpm macos:design --flow add-account
pnpm macos:design --appearance dark
pnpm macos:design:check              # swiftc only
```

Audit a change against System Settings with:

```bash
python3 .claude/skills/design-checklist/scripts/route_review.py \
  --surface desktop-app --artifact preferences --platform macos
```

Then read `.claude/skills/design-checklist/references/platform-macos.md`.

## What “System Settings” means here

The **main window** is a settings-style management app:

```
NavigationSplitView
├─ sidebar: List(selection:) + .listStyle(.sidebar)
│           optional .searchable(placement: .sidebar)
└─ detail:  NavigationStack
            Form { Section { … } }.formStyle(.grouped)
            NavigationLink only for a real child pane
```

A pane is a **grouped Form**, not a dashboard. Compare to
System Settings → Wi‑Fi / Displays / Battery / Privacy & Security:

- One column of inset groups on the system window background.
- Leading label, trailing control (`Toggle`, `Picker`, `LabeledContent`,
  `NavigationLink`, small `Button`).
- Section `header` / `footer` for grouping and explanation.
- System fonts, system colors, system metrics. No palette file.
- Toggles and pickers **instant-apply**. No OK/Apply on a pane.
- Create / inspect / confirm flows are **sheets** with toolbar Cancel +
  primary action.
- Destructive work uses `confirmationDialog` or `NSAlert`, never a
  custom modal card.

The **Settings scene** (`Settings { }` / ⌘,) is only app-wide, rarely
changed preferences (launch at login, usage visibility, security facts).
Task-specific options stay on the pane they affect. Do not put a
Settings row in the main sidebar.

The **menu bar extra** is a status-item menu, not a second app. See
[Menu bar extra](#menu-bar-extra).

## Allowed building blocks

Prefer these, in this order. Use the platform default for fonts, spacing,
tints, and materials. Do not wrap them in a custom “design system”.

| Job | Use |
| --- | --- |
| App / window / prefs | `Window`, `Settings`, `MenuBarExtra`, native `Toolbar` |
| Shell | `NavigationSplitView` + `List` + `.listStyle(.sidebar)` |
| Drill-in | `NavigationStack` + `NavigationLink` + `navigationTitle` |
| Pane body | `Form` + `.formStyle(.grouped)` + `Section` |
| Values | `Toggle`, `Picker`, `TextField`, `SecureField`, `LabeledContent` |
| Commands | `Button`, `Menu`, toolbar `cancellationAction` / `confirmationAction` |
| Empty / search miss | `ContentUnavailableView` |
| Progress | `ProgressView` |
| Confirm | `confirmationDialog` or AppKit `NSAlert` |
| Files | `NSSavePanel` / `NSOpenPanel` |
| Brand mark | Provider / app rows: bundled logo via `DesignProviderIcon`. All other chrome: SF Symbol. No card, no badge plate. |

## Forbidden (this is why previous prompts failed)

Do **not** introduce or copy:

- Web / dashboard chrome: custom cards, rounded containers, gradients,
  glass groups, chip strips, status pills, hairline dividers you drew
  yourself, “hero” headers, metric tiles.
- CSS-like layout: `TraySpacing`, `TrayRadius`, `QuickPanelMetrics`,
  `NativeWindowMetrics`, hardcoded RGB (`anypickViolet`, `anypickAmber`,
  `Color(red:green:blue:)`), `RoundedRectangle`, `overlay` strokes,
  `Color.accentColor.opacity(0.12)` selected rows.
- Custom controls that recreate a system one: handmade sidebar, search
  field, segmented chrome, checkbox, switch, pop-up, toolbar.
- iOS-only habits used as decoration: large circular glyphs, stacked
  title+caption as a substitute for `LabeledContent`, inset cards.
- Pixel-perfect frames as design: `frame(width: 208)`, `frame(width: 336)`,
  `padding(.horizontal, 20)` action bars, magic `minHeight` on every
  empty state.
- Form `Section` cards used as pane actions. Actions sit **under the
  last group**, outside the Form — System Settings → Displays
  (`Advanced…` / `Night Shift…`). Not a second grouped card, not the
  toolbar, not a window footer.
- A product Light/Dark picker. Follow system appearance.
- Secrets, tokens, or raw backup JSON in any view.

If SwiftUI cannot reproduce a System Settings behavior, **bridge AppKit**
(`NSHostingView`, `NSAlert`, `NSOpenPanel`, `NSSavePanel`, `NSMenu`)
instead of faking the control.

## Pane recipe

```swift
Form {
  Section {
    Toggle("Open at Login", isOn: $model.openAtLogin)
    LabeledContent("Status") { Text(statusText).foregroundStyle(.secondary) }
    NavigationLink(value: DesignRouteID.app(app.id)) {
      Label(app.name, systemImage: app.symbol)
    }
  } header: {
    Text("Startup")
  } footer: {
    Text("Shown when the reason is not obvious from the control label.")
  }
}
.formStyle(.grouped)
.navigationTitle("General")
```

Rules:

- One primary job per pane. Progressive disclosure via `NavigationLink`
  or a trailing `Button("Options…")` / `Button("Advanced…")` that opens
  a sheet or child pane — the System Settings pattern.
- Row-level commands stay **in the Form** (trailing `LabeledContent`
  button). Pane-level commands sit **under the last group**, outside
  the Form (`DesignGroupedPane`).
- `List` is for the sidebar and for true selectable collections (logs).
  Do not fake selection with `.buttonStyle(.plain)` +
  `listRowBackground`.
- Window title tracks the pane (`navigationTitle`). Restore the last
  sidebar destination when that state is persisted.
- Sidebar icons are SF Symbols and must follow the **system accent**.
  Fixed colors only when they encode meaning (attention), and never as
  the only signal.

## Sheets

Create / choose / inspect flows (`Add Subscription`, `Add API Gateway`,
`Use With`, `Models`, `Export Accounts`, `Import Accounts`):

- `sheet` + grouped `Form`. No navigation heading.
- Cancel + default sit **under the last group**, same Displays
  pattern. Disable the default until the form is valid.
- Files use `NSSavePanel` / `NSOpenPanel`. Do not draw a fake file-name row.

## Menu bar extra

| Surface | Style | Content |
| --- | --- | --- |
| Production and fixture | `.menuBarExtraStyle(.window)` | Power Quick Panel from `MenuBarMenu.swift`: app routes, attention recovery, direct Proxy Hub/local-proxy controls, switch flow, Settings, and Quit. |

The menu bar is the compact power surface defined by Pencil node `m0s8el`.
Production and the fixture must host the same `MenuBarMenu`; do not replace it
with `AppMenuBarMenu` or a text-only native menu. Service start, stop, and restart
actions stay available in the tray; detailed configuration belongs in the main
window. Production launches the main window as a regular Dock app. Closing the
window never stops proxies; reopening from the Dock or menu-bar restores the
app. `MenuBarPreview` is the isolated window used to inspect the
same rich states without clicking the system status item.

Proxies is collapsed by default to preserve tray height. Its summary row expands
to Proxy Hub and the per-account services with the same start/stop and restart
controls. Attention remains visible in the collapsed summary instead of forcing
the section open.

## Information architecture

Main window sidebar (peer destinations, System Settings style):

| Destination | Job |
| --- | --- |
| Overview | Attention + Get Started + summary. Not a marketing dashboard. |
| Apps | Every switchable installed client/provider. Native-only clients switch saved accounts; only compatible-routing clients expose custom providers and model configuration. |
| Accounts & APIs | Searchable saved logins and API endpoints. **Add Account** owns importing the current local login and provider sign-in; there is no separate “Save Current Login” toolbar action. |
| Proxy Hub | One local endpoint, its sources, published models, and service lifecycle. |
| Proxies | Per-account translation processes and their service lifecycle. |
| Activity | Searchable events and live logs in a bounded, independently scrolling region. |
| Account Backups | Export / import saved accounts. |

Settings scene (⌘, — **not** a sidebar row):

| Pane | Job |
| --- | --- |
| General | Open at Login, Getting Started, About. |
| Usage | Quota visibility + low-usage warning. |
| Proxies | Start-at-launch policy. |
| Security | Always-on facts (loopback, no secrets in UI, backup warning). |

Do not duplicate a toggle across General and a specialized pane.
Do not put appearance, scrolling, or other **system** preferences here.

Usage rows preserve every provider-reported window and label. Never rename the
first window to “5-hour” or infer a reset period. Idle proxies are neutral; they
only become global attention when an active app route or running Hub depends on
them and the service is unavailable.

## Behavior that is not visual

These still apply; chrome must not fight them:

- Instant-apply for pane toggles and pickers.
- Single-flight mutations in production (`TrayStore`). Disable controls
  when `isBusy`.
- Errors: `NSAlert` / `.alert` only. Success is silent or VoiceOver +
  Edit → Undo — not a toast stack.
- Backups: `NSSavePanel` / `NSOpenPanel`. Files contain credentials;
  say so before create / import.
- Proxy addresses are loopback; never show the auth token.
- Reduce Motion and Increase Contrast come from the system. Do not add
  a motion or contrast switch.

## Known leftovers

These are smaller than the chrome bugs above. Do not copy them into new
panes.

- Comments citing `HANDOFF` / Pencil frames — delete when touching the
  file.
- Do not reintroduce leftover tray cards, RGB tokens, or `Native*` chrome.

## When you are done

- The pane is a grouped `Form`. A screenshot next to System Settings
  should share control anatomy, not just “a sidebar”.
- No new token file, card, or pixel recipe.
- `pnpm macos:design:check` compiles.
- Isolation tests still forbid supervisor / `~/.anypick` references in
  this tree.
- If a string-scan test demanded a Pencil width or a removed wrapper,
  update the test to the contract above.
