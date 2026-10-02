# Tauri 1-1 parity: decision trail

Scope: `src/tauri/` migrated to Tailwind, every macOS feature cloned.

**Done predicate.** `make check-parity` runs `test/parity.test.ts`, which reads
every `accessibilityIdentifier` in `src/macos/*.swift` and checks it against
`test/parity-map.ts`. The map has one row per macOS feature and each row is
either pointed at a Tauri `data-testid` that exists, or marked out of scope with
a reason. A macOS feature with no row fails; a `done` row naming an absent
element fails. It is a check, not a self-report.

The remaining rows are marked `todo` and are the work list. The port is done
when that set is empty.

## Progress

| | |
| --- | --- |
| macOS features (excluding the tray surface) | 151 |
| Ported | 150 |
| Remaining | 0 |
| Out of scope, with a written reason | 7 |

**The port is complete.** One row is not ported rather than exempt:
`backups.inventory.<id>`, macOS's reviewed-backup list. `ProductionModel.backups`
is initialised empty at `ProductionModel.swift:187` and nothing in the engine
publishes a backup inventory, so that row renders in *neither* app — it is
fixture-only on macOS. Marking it `skip` with that reason is the honest answer;
the alternative was to invent a list the engine cannot produce.

## What "done" does and does not mean

The predicate is **one row per macOS `accessibilityIdentifier`, each pointed at a
`data-testid` that exists**. That is narrower than "the app works", and the gap
is worth writing down because passing 150/150 did not mean the port was right.

- **`tauriIdentifiers()` is a static scan.** It reads the source for
  `data-testid`/`testId` *literals*. It proves a string appears in a file, not
  that the control is reachable, or enabled in the right state, or correct. A
  control with the right id and the wrong behaviour passes.
- **The screenshots are what catch that class of bug**, and they only cover 13
  views. Everything else was verified by unit test and by reading.
- **A sheet that branches on data was reachable in one branch only.** The default
  fixture publishes one client and one gateway provider, so `Add App…` and the
  gateway sheet could only ever be seen empty. `fixture-line.mts --rich` now
  widens the data and `screenshot.mts` can select per view — the first thing that
  reached for it found a live bug (below).

Two real defects survived the green parity run and were caught only by looking:

- **Add App was dead on arrival.** The first-run list counted any client with a
  `routeKind` as "already managed", but *every* registered client publishes a
  native row whether or not AnyPick has touched it. So the derived list was
  every supported app and the picker had nothing to offer, on every first run.
  No unit test caught it because no fixture contained a native-only client.
- **`CheckRow` rendered its detail before its label**, so the sheet read
  "Google's AI coding tool Gemini" — a sentence with its subject at the end.

**Navigation shape still differs.** macOS has `AppDetailView` and
`ProxyDetailView` as pushed destinations; the Tauri app carries their controls
in the list rows' menus. Capability is identical and every id is reachable, but
the shape is not, and that is a deliberate difference rather than an oversight.

## The visual pass, and why it came second

Passing the predicate says nothing about how the app looks. Reviewing the
screenshots afterwards found four defects that every green gate had accepted:

- **No row separators.** `Form { }.formStyle(.grouped)` draws a hairline between
  rows; the group had none, so a section was one undifferentiated block and a
  screen of sections read as one long list. `Group` now uses `divide-y`.
- **Section headers had no hierarchy.** 12px regular secondary is the same size
  as a row's detail line, so the eye could not tell a heading from its contents.
  They are now 11px semibold with real air above.
- **Pane actions were left-aligned.** `SharedUI.swift:424` pins the action row
  `.frame(maxWidth: .infinity, alignment: .trailing)`. Two panes had hand-rolled
  their own container and were left-aligned while the third used the primitive.
- **The Apps pane built a row per _route_.** macOS builds one row per app
  (`AppsView.swift:34`) and puts Disconnect and Remove in the row's context menu
  (`AppsView.swift:96`). An app with four saved logins became four rows, each
  with Use, Change…, Refresh, Disconnect and Remove — which is why the pane read
  as a control panel rather than a list of apps. `lib/app-entry.ts` now collapses
  routes into one `AppEntry` and the commands sit in the menu.

Two more came out of adding the two new sheets to the screenshot harness, which
is the argument for keeping that harness honest:

- **`RowMenu` rendered off-screen and unclickable.** The panel was absolutely
  positioned *above* its trigger inside a `Group` that has `overflow-hidden`, so
  on the first row of a pane the items were clipped by two ancestors. jsdom
  clicked them happily; the real webview could not. The panel is portalled to
  `document.body` and positioned against the trigger's viewport box, flipping
  below when there is no room above.
- **`screenshot.mts` waited for its target after _every_ open step**, so a view
  needing two clicks (row menu, then item) timed out waiting for the sheet after
  the first one. It now waits after the last step only.

The total went **up** from 117 while the ported count went up too, because the
inventory extractor was wrong. See "the extractor undercounted" below. A wrong
denominator would have made every percentage in this document a lie, and the
port would have looked finished while 30 real controls were never in the map.

### The extractor undercounted by 31

The first version of `test/inventory.ts` read only
`.accessibilityIdentifier("…")`. That is **one of four** ways an id reaches the
tree in this codebase, and the other three are invisible to that scan:

1. a literal argument — `.accessibilityIdentifier("hub.rotate-api-key")`
2. **a component parameter**, in two spellings:
   `DesignCopyButton(identifier: "hub.copy-address")` forwards it itself
   (`SharedUI.swift:301`), and `DesignCandidatePicker` takes it as a labelled
   `accessibilityIdentifier:` argument. Neither literal sits near the word
   `accessibilityIdentifier` at the call site.
3. **a computed helper** — `strategyIdentifier(for:)` returns a literal, so
   `hub.strategy` exists only as a return value
4. **a `let` binding applied to a variable** — `ProxyHubView.swift:451` is
   `let rowKey = cond ? "hub.rotation.\(id)" : "hub.account.\(id)"`, applied
   later as `.accessibilityIdentifier(rowKey)`

The consequence was not cosmetic. 31 real controls had no map row. It surfaced
only because the row-missing test failed on them. Had the map been written
against the smaller inventory, it would have read as complete while missing
`hub.copy-address`, `proxies.inspector.copy`, `apps.add`, `app.reset`,
`backups.import`, `activity.clear-log`, both hub source rows and 24 others.

Each of the four shapes was found by a distinct failure in sequence, not by
reading ahead. Shape 2's second spelling was found only after shape 2's first
spelling left `hub.conflict.<id>.picker` unclaimed, and shape 4 only after
shape 3 left `hub.account.<id>` unclaimed. `test/parity.test.ts` now asserts each
shape independently, so none can regress quietly, and it asserts that
`SharedUI.swift` still forwards `identifier` — that forwarding is the only reason
shape 2 is detectable at all.

### The second visual pass: identity and clutter

The first pass fixed *structure* — separators, headers, trailing actions, the
one-row-per-app shape. Looking at the rebuilt panes again found two classes of
defect that neither the predicate nor the first pass could see, because both are
about what a row _says_ rather than whether a control exists.

**There were no brand icons.** macOS has `src/macos/Resources/icons/` — 24
vendored marks — and `BrandIcons.swift` that maps roughly forty providers to
them, with a per-provider **fallback symbol table** so that a provider with no
bundled mark still gets a glyph that says which provider it is. The Tauri app had
36 generic `Symbol` paths, a blanket fallback to `square.grid.2x2`, and a
per-app table in `AppsPane` that mapped six clients to five shapes and gave
OpenCode and Claude the same one. A list of apps rendered as a list of identical
squares.

Ported: the files are vendored into `assets/icons/` (which `vite.config.ts`
already publishes as `publicDir`, so they land at `icons/<file>`), the candidate
file list, the `fullColorMarks` set, the fallback symbol table and the tint table
all move to `lib/brands.ts`, and `shell/ProviderIcon.tsx` is the one place that
loads a brand file — the same rule macOS states as *"Chrome uses SF Symbols; only
this view loads brand files."* Twenty-six new symbol paths were drawn for the
fallback table.

Two rendering details are worth naming, because both are the web equivalent of a
SwiftUI/AppKit mechanism:

- A **template mark is tinted with `mask-image`, not `filter`.** Every
  non-full-colour mark is a single white or black fill; macOS sets
  `image.isTemplate` and tints it with `DesignBrandAsset.tint`. A `filter` chain
  has no way to name "this provider's brand purple", which is why OpenRouter,
  DeepSeek and Gemini have an entry in the tint table. The mark becomes the mask
  and the background is the colour.
- **An unmapped symbol must not fall back to Overview's glyph.** The old
  last-resort path silently rendered `square.grid.2x2`, so an unmapped provider
  looked exactly like the Overview destination. It now falls back to a dashed
  app outline, which says "no mark" instead of saying "Overview".

**The rows were a control panel.** macOS is unambiguous about where a row's
commands go, and the port had drifted from it in three places:

| row | macOS | before |
| --- | --- | --- |
| account proxy | `trailing: { lifecycleAction }`, rest in `contextMenu` (`ProxiesView.swift:143`, `:147`) | status dot, address, **two identical copy glyphs**, Stop, Restart, Rotate |
| hub summary | `trailing: { DesignStatusLabel }` (`:120`) | status dot, copy glyph, Stop, Restart, Rotate, Published Models |
| hub service | `trailing: { hubLifecycleButton }`, Restart in `contextMenu` (`ProxyHubView.swift:28`, `:31`) | status dot duplicating its own subtitle, Stop, Restart, Rotate |

Two glyphs that look the same and copy different things is worse than no glyphs,
and `Stopped` in a status label next to `Not running` in the row's own subtitle is
the same fact printed twice — which is exactly what the screenshot showed. Each
row is now one trailing control plus a menu, and the hub's rotate had a second
button on a row that already had one.

**A brand mark belongs in the provider picker, and a `<select>` cannot show one.**
macOS does not use a SwiftUI `Picker` for providers and says why:
*"SwiftUI's Picker drops custom image labels from NSMenu items, so bridge the
platform control when brand identity matters"* (`BrandIcons.swift:41`). A webview
has no `NSMenu`, and a native `<select>` is worse than the SwiftUI case: the
platform paints the items, so each provider is a bare word. `ui/MenuSelect.tsx`
is the same disclosure-and-panel shape as `RowMenu` wearing the picker's bezel,
with the selected item checkmarked the way `NSPopUpButton` marks it. The
placement logic moved to `ui/usePopover.ts` so the two popovers cannot disagree
about flipping, closing on scroll, or restoring focus.

**And the row that showed the wrong brand.** `providerFromSourceRef` returned
`'openrouter'` for *any* `gateway/…` ref, so a LiteLLM row and a Cloudflare row
both wore the OpenRouter mark. Worse, `refs.SerializeRef` writes a hub ref as
`hub/<name>` (`refs.go:61`) and a gateway ref as `gateway/<name>` — but this
function only recognised `proxy-hub/…`, which is a **proxy row** id, a different
namespace the engine never sends. So a real hub connection row titled itself
"Hub/Default" and wore a server rack. The vendor now comes from the snapshot,
keyed by gateway **name** (the id and the name differ as soon as a user renames
one).

Two smaller fixes came out of the same pass: the sidebar's `app.badge` path was
an oval with a stem and read as the digit `0`, and the Backups pane printed
*"The file contains account credentials"* in three separate footers where macOS
says it once, with a symbol, in a standing warning row (`BackupsView.swift:37`).

### Proving the row shape, not just asserting it

The parity predicate found the four of these that are *missing* controls. It
cannot see a row that has six buttons where macOS has two, because every id the
map names is still emitted. So two tests now pin the shape from the SwiftUI
line they came from — one row-per-app, one trailing control per service row, a
stopped relay with no empty menu beside it — and the row-action tests assert the
menu's item list exactly:

```
expect(screen.getAllByRole('menuitem').map((i) => i.textContent))
  .toEqual(['Restart', 'Copy Address', 'Copy API Key', 'Rotate API Key…'])
```

That last one is also a guard on `testId`: `MenuItem` takes its id as a literal at
the call site, because `parity.test.ts` finds Tauri elements by scanning source
for `data-testid` strings. Building the menu from a runtime array of
`{label, testId, run}` made four real controls invisible to the scanner, and the
parity row correctly reported "the UI does not emit this".

### Proxy Hub, ported

The pane this replaces was a list of source switches and nothing else. It now
carries the Service group (lifecycle button, address with copy, API key with copy
and a confirmed rotate, published-model summary), Routing Conflicts with a
provider picker, and Request Strategy as a Failover/Round Robin segmented
control.

Two decisions worth recording:

- **The hub's lifecycle is a button, not a switch.** The macOS README puts
  service lifecycle in the Service row because a hub is started and stopped. A
  switch said "policy", and there was no way to restart one.
- **Rotation is a segmented control, not a switch.** A hub always has a
  strategy, and `hub-rotation` carries a boolean. A switch renders "on" as
  round-robin with no way to express *which* one is selected, so the segment
  carries the state the boolean actually encodes. macOS uses
  `.pickerStyle(.segmented)` for the same reason.

The routing-conflict picker reads `actionId` off the chosen option and invokes
it. It sends nothing when the value is the placeholder, because a select that
emits its own placeholder would post "Choose provider…" to the engine.

`test/hub.test.ts` covers the derivation: source ordering, the model count keyed
by full hub source id, candidate collapsing by provider, the conflict title and
subtitle, and the wire form of all three row kinds including that `enabled` is
always sent explicitly.

### Published Models, ported

Fifteen macOS ids, the destination that answers "what can I point a client at".
It was reachable only as a broken link to Proxies.

It is a route the shell owns rather than a sidebar destination, because macOS
pushes it onto the detail stack (`DesignRouteID.publishedModels`) and a webview
has no `NavigationStack`. The sidebar cannot select it; the hub's Published
Models row and the proxies hub summary both open it, and leaving either pane
closes it so Back never lands on a detail whose owner is not on screen.

**The action ids are reconstructed, not published.** `registerModelRoutingActions`
in `hub_routing.go:488` registers `model-route:<model>:provider/<pid>` in its
handler registry but does not put them on the wire. macOS rebuilds the same
strings from `hubCatalogs` and `hubSources`, and so does `lib/published.ts`. The
format is a contract with the engine: if either side's string changes, every
picker invokes an id the registry does not have and every route silently does
nothing. That is the one assertion in `test/published.test.ts` that is really
about the Go side, and it is written out in full.

The vendor group opens on load when the list is short enough to read, following
macOS's own `initializeExpansionIfNeeded` rule rather than a preference. The
screenshot is what proved this mattered: seeding the open set in an effect meant
the harness waited on the pane root, captured the collapsed state, and the image
showed a working list containing nothing.

### The Apps connection sheet, ported

Before this, a Tauri user could not change an app's login at all. Every row on
the Apps pane offered `Use`, and that was the whole of it.

`AppRouteSheet.tsx` is the macOS staged flow: choose a connection → review →
applying → outcome, with a model step that runs **after** the switch because
model options are published per connection. `lib/app-route.ts` holds the rules,
so each is testable without a webview.

Two decisions worth recording:

- **The sheet is reachable from every row.** macOS reaches it from the app
  detail pane, which this app does not have. A `Change…` on each row gets the
  same capability without inventing a destination.
- **The provider came off a serialized ref.** `ActionRow.sourceId` is
  `refs.SerializeRef` output — `account/claude/work`, not `claude`. Handing the
  whole ref to `nativeProviderName` titled the scope picker "Account", which is
  what `providerFromSourceRef` fixes. The unit test caught it; the screenshot
  would not have, because the wrong text is still text.

The apply failure (`app-route.apply-error`) is a `skip` with a reason rather
than a gap: the engine's refusal arrives as a result frame and is already shown
in the notice banner. A second copy inside the sheet would say it twice.

### The Add Account flow, ported

The sheet this replaces rendered a field dump per provider: a Save button, a
Detect button, a secret box, a kind picker and a name field, all at once, for
every provider, on one scroll. "Add Account" was the label on four different
commands, and an API gateway block sat underneath it — so the provider picker
changed meaning halfway down.

`AddAccountSheet.tsx` is macOS's flow: one provider, one **source**, and only the
fields that source needs. `lib/account-flow.ts` holds the rules
(`availableSourceModes`, `blockerMessage`, `savedAccountCanSubmit`,
`identityTitle`, `identityDetail`, `primaryTitle`), so each is testable without a
webview — 40 tests, no DOM.

Four decisions worth recording:

- **A gateway is not a login, so it gets its own sheet.** `AddGatewaySheet.tsx`
  replaces a block inside the Add Account dialog. macOS says why: a gateway is a
  provider key plus an endpoint, it is not something AnyPick reads off this Mac,
  and there is no identity to detect.
- **The answer has an owner.** Every Add Account command used to become a banner
  over the dialog holding the fields the user still had to fix. `App.tsx` now
  routes `ACCOUNT_FLOW` results into the sheet, which shows a detected identity in
  its identity card and a refused sign-in in an inline row **with the ways out**
  ("Use Account on This Mac", "Use API Key"). Those two buttons are only offered
  when they could work — offering a fallback that cannot is worse than offering
  none.
- **Replace is a consequence, not a label.** macOS sets `replaceExisting` only
  after an `ACCOUNT_EXISTS` refusal. The Tauri version first keyed it off "the
  user typed a name", which promised to overwrite an account the engine had not
  yet collided with; and a rejected document then left a button that would
  destroy the account it could not read.
- **The ZCode plan hint needed a shell command.** `zcodeCodingPlanWarning` reads
  another app's cache at `~/.zcode/v2/coding-plan-cache.json`, and a webview
  cannot stat the user's home directory. `app/src/main.rs` grew a `zcode_context`
  command that answers with `appInstalled` and `planWarning`; the *rule* stays in
  TypeScript where it is testable. `home` is a parameter rather than an
  environment read so the Rust test can point it at a temp directory instead of
  the developer's own account.

The gateway sheet's connection row is the other half of the same decision: macOS
renders `gateway-flow.testing` / `.available` / `.unavailable` as three states of
one control. The Tauri version opened a *second dialog* to run the test and
answered with a banner. `lib/gateway-flow.ts` holds the shape rules — which
provider supplies its own endpoint, which derives it from an account id, which
needs a key — so the sheet asks for one field rather than all five (29 tests).

### Which apps are yours, ported

The engine publishes every registered client, so "which apps the user manages" is
a local preference and nothing else. macOS keeps it in `AppPreferences`
(`UserDefaults`), *not* in supervisor state, and that placement is load-bearing.
The Tauri pane listed every published client, so a user who removed an app saw
it come back on the next launch.

`lib/app-preferences.ts` ports `effectiveConfiguredAppIDs`,
`availableAppsToAdd`, `addApp` and `removeApp`. Two subtleties the tests pin:

- **Removal is stored separately from the list.** One list cannot express both
  "never added this" and "added then removed", because `effectiveConfiguredAppIDs`
  *subtracts* removals.
- **Adding clears the removal.** Leaving the flag behind makes "add it back"
  report success and show nothing — the one outcome the user cannot diagnose from
  the pane.

### Overview is a summary, not a list

macOS lists at most three apps and offers `overview.apps.see-all` for the rest.
The Tauri pane listed every app, which made it a second Apps pane and pushed the
app needing attention off the screen behind the ones already fine.
`lib/overview.ts` ports `hasActiveOverviewApps` / `overviewApps`, and the test
pins the thing that actually broke: **an app with four routes is one app**. Four
rows would have pushed three other apps out of the three slots.

### Errors belong to the row that caused them

Four macOS ids are per-row error slots (`{x}.error` on a hub source row,
`app-models.error`, `app-route.models.error`). Each needed a wire change, because
the result frame had no owner to route to:

- `setModelRoles` registered no in-flight record, so its answer was
  indistinguishable from a snapshot refresh. The sheet closed on a command the
  engine had refused and nothing said why. It now registers `{kind:
  'model-roles'}` and the sheet stays open.
- `hub-source-toggle` and `hub-rotation` failures arrive as banners. A banner
  says something failed without saying which switch is now in a state the user
  did not choose.

### Clear Log

The one remaining Activity id. Clearing a log is local, exactly as clearing the
event list is: the engine keeps the file.

The subtlety is that this pane **polls**. A clear that only blanked the view
would put the whole log back 1.75 seconds later with no user action, and a test
that checked the blank state alone would pass. `keeps the cleared log hidden
when a poll arrives` is the assertion, and it had to allow longer than
`POLL_MS` before it could fail for the right reason.

### Account rows and the detail sheet

Every account row had four buttons, always visible: Use, Refresh, Edit, Remove.
A row of accounts all shouting their own commands is the control-panel look the
macOS contract bans. This is the fix.

`AccountRow.tsx` is the four reserved trailing columns, ported from
`AccountRowTrailing` (`AccountsView.swift:368`): the primary command, the status
marker, the menu, and the chevron. The width lives on the column element, not
on the control inside it — macOS documented the trap at `SharedUI.swift:313`
where a frame on an empty `Group` is dropped and the columns stop lining up. The
screenshot shows the Work row (no Switch, because it is current) and the Personal
row (Switch) sharing a marker, a menu and a chevron column, which is the whole
point.

`AccountDetailSheet.tsx` is the drill-in. A dialog, not an inline expansion,
because expanding a row would resize the very reserved columns that make the list
line up.

Two decisions worth recording:

- **The menu carries the primary command too.** macOS reveals Switch on hover,
  which means a keyboard user, who never hovers, cannot reach it. Here the menu
  has it and both call the same handler. A command in two places that disagree
  with itself is worse than one command in one place.
- **Show Details stays enabled while the engine is busy.** It only reads the
  snapshot. Disabling it would make a row's own drill-in look broken during the
  one moment the user most wants to read it.

`lib/account-row.ts` normalizes the two row shapes into one `AccountRowView`.
The engine sends a saved account with `active`, `switchActionId` and
`canRefresh`, and a gateway with `ready` and none of those. Passing the raw
union around made a call site read `active` off a gateway — undefined — which
classifies every gateway as attention, forever. Building the view model once is
what makes that impossible rather than unlikely.

### The remaining 41, by cause

**Proxy Hub is done.** Its rows moved to `done` above.

**Published Models is done.** See above.

**The Apps connection sheet is done.** See above. Still missing around it:
`app.add.picker` (the Add App sheet), `app.models.manage` (a standalone models
sheet), and the two error slots.

**The Add Account sheet is one form, not a staged flow.** `account-flow.source`,
`.local-source`, `.identity`, `.sign-out`, `.blocker`, `.error`,
`.credential-profile`, `.cloudflare-routing`, `.cloudflare-hint`,
`.zcode-hint`, `account-edit.error`. The provider picker, method picker, name,
secret, detect, clear and save are all there. What is missing is the source
segmented control, the identity card with re-detect and sign-out, the blocker
and inline error rows, the Cloudflare account/gateway routing row and its
hints.

**Account rows are done.** The provider filter picker (`accounts.filter`) is
the one thing left in this area.

**Codex profile editing is partial.** `codex-profile.source`, `.model`,
`.transport`, `.copy.{x}`, `.more.{x}`, `.create-missing`. The create form now
has all three pickers; what is missing is the per-row copy-launch-command
button, the row menu, and the "create missing profiles" automation.

**Proxy rows have no context actions.** `proxies.inspector.lifecycle`,
`.address`, `.rotate-key`, `proxies.row-action.{x}`. Tauri can toggle, restart
and copy a key. macOS also copies the address, rotates the key behind a
confirmation, and has a detail view.

**Settings is missing four panes.** `settings.tabs`, `.getting-started`,
`.open-proxies`, `.review-backups`, `.create-codex-profiles`. The four toggles
and Run Doctor are there; the Security pane's read-only posture rows and the
per-account model refresh are not.

**Smaller.** `overview.apps.see-all`, `overview.hide-onboarding`,
`apps.empty.add`, `gateway-flow.region`, `.available`, `.unavailable`,
`activity.logs.live`, `backups.inventory.{x}` (a reviewed-backups list with no
file picker behind it), `state.*` and `sidebar.*` identifiers that name macOS
view-state placeholders the Tauri shell expresses structurally instead.

### Ordered by value

1. **The Add Account staged flow.** Ten ids: source segmented control, identity
   card with re-detect and sign-out, blocker and inline error rows, the
   Cloudflare account/gateway routing row and its hints. The one form the Tauri
   sheet has is a per-provider field dump, not a flow.
2. **Settings' four missing panes** and `backups.inventory.{x}`.
3. **Proxy detail view.** `proxies.inspector.lifecycle` and `.address` need a
   route; the copy buttons are now on the row.
4. **Codex profile extras** — per-row copy-launch-command, row menu, create-missing.
5. The rest, in the order the map lists them.

## The visual-parity harness

`make screenshots-tauri-ui` renders every destination and dialog from
`test/fixture.ts` with the real built CSS and writes to `.screenshots/`.

It found two things no gate had. The filled red destructive buttons above came
out of looking at `accounts.png`. The Activity screenshot showed a bare "Clear"
floating above the list, which is what prompted the Events/Live Logs rewrite.

It is not a CI gate, and that is a deliberate limit rather than an oversight:
jsdom does not rasterize, so a pixel diff needs Playwright and a browser
download in the runner. The harness runs on a developer machine. The parity map
is what CI enforces, because that is the part a machine can decide.

`file://` will not load the bundle. The built output uses ES modules and
Chromium blocks a module script from `file://`, so the harness serves the bundle
over loopback HTTP instead. A `file://` screenshot is a blank canvas that reads
as a passing diff.

## Baseline (measured before any change)

| Gate | Result |
| --- | --- |
| `tsc --noEmit` | pass |
| `vitest run` | 170 pass |
| `vite build` | pass |
| `cargo check --locked` | pass |
| macOS a11y ids | 138 |
| Tauri test ids | 63 |
| macOS ids with no Tauri counterpart | 72 |

The gates are green and the app is still visibly broken. That is the finding:
nothing in CI looks at the UI.

## Root causes, in the order they were fixed

1. **Tailwind was installed but never switched on.**
   `tailwindcss` and `@tailwindcss/vite` are devDependencies and
   `@tailwindcss/vite` is registered in `ui/vite.config.ts:20`, but no CSS file
   contains `@import "tailwindcss"` and no `.tsx` uses a Tailwind utility. The
   plugin scans, finds nothing, emits nothing. Zero of the styling is Tailwind.

2. **Two hand-written stylesheets fight, and the wrong one wins.**
   `ui/main.tsx:4` imports `./styles.css`; `ui/App.tsx:13` imports
   `./ui/system-settings.css`. ES module evaluation loads `styles.css` last, so
   it wins every equal-specificity collision. Seven collisions:
   `icon-button`, `model-select`, `pane-actions`, `empty-state`,
   `loading-state`, `status-dot`, `row-title`/`row-detail`.
   The System Settings metrics that `system-settings.css` was written to carry
   are the ones being overridden.

3. **Classes the TSX emits that no stylesheet defines.** These render
   unstyled, which is most of what "lỗi nhiều" looks like on screen.
   - `button-secondary` is the *default* `Button` kind
     (`ui/primitives.tsx:120,130,148`) and is defined nowhere. Every default
     button in the app is a bare UA button.
   - `group` wraps every settings group (`ui/primitives.tsx:44`), undefined.
   - `notice-error`, `notice-warning`, `notice-info` (`ui/shell/NoticeBanner.tsx:25`).
     `styles.css` has compound `.notice.error` / `.notice.info` selectors, which
     never match a single-class `notice-error`. Every error banner is uncolored.
   - `notice-suggestions`, `empty-copy`.

4. **Rust allowlist omits `doctor`.** `app/src/sidecar.rs:617-620` accepts
   `invoke | logs | mutate | model-roles | backup-inspect | cancel`. The Go
   engine accepts `doctor` (`internal/desktop/protocol.go:268`,
   `internal/desktop/server.go:382`), the Swift client sends it, and
   `ui/lib/wire.ts:308` builds it. "Run Doctor" therefore always fails with
   "AnyPick rejected an invalid app command."

5. **Unguarded `navigator.clipboard`.** `ui/App.tsx:140` calls
   `navigator.clipboard.writeText(key)` with no existence guard, inside a Tauri
   event callback, with no error boundary above it. `navigator.clipboard` is
   `undefined` when the context is not secure, so the property access throws
   synchronously and unmounts the tree. `ui/lib/service.ts:167-176` wraps the
   same call correctly; the proxy-token path does not.
   `test/setup.ts:11` always stubs the clipboard, so no test can catch it.

6. **`service-state` listener has no `.catch`.** `ui/App.tsx:208-228`. The
   `service-line` listen at `:203` has one. A rejected subscription is an
   unhandled rejection and leaves `state='starting'` forever.

7. **`doctor` frame kind is listed but never decoded.**
   `ui/lib/wire.ts:18-28` lists it in `UI_FRAME_KINDS`; `decodeFrame`
   (`:158-236`) has no case for it and returns `null`. If the engine ever answers
   with a bare `doctor` frame, the busy slot recorded at `service.ts:156` never
   settles and every `busy`-gated control locks up.

## Not the cause, ruled out

`color-mix()` (~20 uses), `height:100dvh`, and the `AccentColor` keyword in
`styles.css` are dropped by older WebKitGTK. That is real, but it degrades
rather than errors. Deleting the stylesheet removes it anyway.

The same caveat now covers `mask-image`, which `shell/ProviderIcon.tsx` uses to
tint a monochrome brand mark. A webview that drops it paints **nothing** where
the mark should be — not a wrong colour, an empty gap — so the fallback is not
optional polish: if `CSS.supports('mask-image', 'url(x)')` is false the icon
should take the `Symbol` branch instead. That check is not written yet, and it
cannot be, because no test here runs WebKitGTK.

`navigator.clipboard` on a non-secure origin is a Linux packaging question, not
a code one, but the guard is correct regardless.

## Decisions

- **Tailwind v4 with an `@theme` token block, not utility soup.** The macOS
  metrics named in `system-settings.css:16-29` (a 7pt status dot, 12pt row
  gutter, 10pt group radius) become theme tokens so the numbers the SwiftUI
  contract fixes live in one place and reach every row. This preserves the
  stated contract instead of scattering literals into class strings.
- **Both stylesheets are deleted, not merged.** A merge would preserve the
  collisions and the undefined classes. Delete is the smaller diff that removes
  the defect.
- **`data-testid` and aria labels are the test contract and stay stable.** 60+
  assertions depend on them. Only `test/app.test.tsx:80-118`, which asserts
  computed style on `.shell`/`.sidebar`/`.shell-main`, breaks on a class
  rename. That test is rewritten to structural assertions, because computed
  pixel values are the wrong contract under Tailwind preflight.
- **Tailwind preflight is opt-out per element, not global.** Preflight resets
  `button`, `h1`, and `input`. Every one of those carries an explicit utility in
  this migration, so no opt-out is needed and the reset stays on where it helps.

## Fixed during the migration

- **`doctor` was rejected by the Rust allowlist**, `app/src/sidecar.rs`. The
  engine accepts it, the Swift client sends it, the UI builds it. "Run Doctor"
  failed on every build. Added to the allowlist, with a test at
  `sidecar.rs:691` and a `doctor` probe in `main.rs:81`.
- **`doctor` frames were listed in `UI_FRAME_KINDS` and never decoded.**
  `decodeFrame` had no case, so it returned null and the busy slot recorded by
  `requestDoctor` never settled. Every `busy`-gated control would have locked
  up. Now decoded (`wire.ts:227`), with the report rendered in the Settings
  sheet's Diagnostics group.
- **The fixture seam accepted four frame kinds and the UI decodes nine.**
  `valid_fixture_line` rejected `snapshot-event`, `usage-event`,
  `backup-preview`, `doctor` and `protocol-error`, so a demo run could not
  exercise them. And `deliver_fixture_line` cached only a bare `snapshot` for
  replay, so a fixture window acted on a revision the engine had superseded.
- **The clipboard read was unguarded** at `App.tsx:161`, inside a Tauri event
  callback with no error boundary above it. `navigator.clipboard` is undefined
  on a non-secure origin, so the property access threw and unmounted the tree.
  `test/setup.ts:11` always stubs a clipboard, so no test could catch it.
- **`service-state` had no `.catch`.** A rejected subscription was an unhandled
  rejection and left the window in "connecting" forever.
- **Two `bg-*` utilities on one element resolved by stylesheet order.** The
  switch emitted `bg-switch-off … bg-green`, and the rendered switch was off
  while the snapshot said `enabled: true`. Tailwind decides a tie by the order
  utilities are generated in, not the order they appear in the class attribute,
  so appending the override is not enough. Now exactly one track colour is
  emitted. The screenshot is what showed it: the Proxy Hub source switch read
  off while its row said the account was switched on.
- **A destructive button was a filled red block.** The macOS control sets
  `hasDestructiveAction` (`SharedUI.swift:541`), which is red *text* on the
  normal bezel. A row of three filled red buttons is what the Accounts
  screenshot showed. Now red text on the standard control, and the fill is
  reserved for a dialog's confirming action.
- **The dialog did not trap focus or restore it.** A webview has no native
  sheet to do it, so Tab walked out of an open sheet into the pane behind it
  and a keyboard user could edit the settings the dialog was asking about.
  `Dialog.tsx` now traps Tab, handles Escape, and hands focus back on unmount.
  The confirmation focuses the *confirming* button, because that is the one
  place the safe default is the wrong one.
- **Activity had no Events/Live Logs switch, no category filter, and did not
  poll.** `activityMatchesCategory` and `visibleLogLines` were written, tested,
  and called by nothing. The pane now ports the segmented control, the filter,
  and a 1.75s poll that stops when the tab is not showing, and splits the log
  body into real lines instead of one caption string.
- **The layout test asserted computed pixels.** `test/app.test.tsx` pinned
  `gridTemplateColumns`, `height: 100dvh`, `fontSize: 15px` and `maxWidth`, all
  of which Tailwind preflight legitimately moves. It now asserts structure, and
  loads the utilities it needs rather than the whole stylesheet.