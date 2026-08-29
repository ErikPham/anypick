# ADR-0017: Providers own browser login methods; official methods are preferred, not required (AUTH-01)

- Status: accepted (amends [0015](0015-declarative-compatible-provider-plugins.md))
- Date: 2026-08-13

## Context

Saving another subscription account previously required AnyPick to clear the
current native credential, tell the user to open the provider's tool, and wait
for a later `--current` import. Codex, Claude Code, Kiro, and Grok already ship
login-only commands that open their provider browser flow and write the normal
credential store, so the manual hand-off adds work without creating a stronger
security boundary.

AnyPick must not own vendor OAuth client ids, authorization endpoints, consumer
cookies, or device codes. It must also remain extensible: a full provider plugin
may have a legitimate community login workflow even when no official launcher
exists. Treating “official” as a whitelist would turn a risk preference into a
core authorization policy and block that extension path.

## Decision

1. **Browser login is a provider capability pair.** A full `Provider` may expose
   `loginMethods()` and `login(context)`. Each method has a stable id, display
   metadata, availability, optional native source, and `official` or
   `community` provenance. Core and UI do not hardcode provider ids, OAuth
   endpoints, or commands.
2. **Official is a preference, not a permission boundary.** Providers may mark
   one method preferred. AnyPick orders official methods first, honors the
   provider preference within the same provenance, and chooses an unambiguous
   official default. An explicitly selected community method remains usable. UI
   labels provenance and explains that provider terms still apply.
3. **Built-ins delegate to provider login-only launchers.** AnyPick invokes the
   installed Codex, Claude Code, Kiro, or Grok login command. The launcher owns
   the browser protocol and writes its usual credential location. Child output
   is discarded because it may contain an authorization URL or one-time code;
   those values never enter tray or JSON output.
4. **Login and snapshotting are one compensated provider-scoped mutation.** The
   account service takes the provider lock, checkpoints the exact live login and
   active pointer, saves and locally clears the previous login, runs the selected
   browser flow, verifies a usable credential, commits it through `writeMeta`,
   and updates the active pointer. Any failure restores the checkpoint. The
   safety rules from ADR-0004, ADR-0009, and ADR-0011 apply unchanged.
5. **Source selection remains explicit.** Providers with multiple native stores
   expose methods scoped to a source or require a caller-supplied source. Core
   does not infer a source from the provider registry.
6. **Declarative compatible providers remain key-only.** This ADR does not give
   ADR-0015 definitions JavaScript hooks or permission to import web sessions.
   A provider that needs browser login implements the reviewed full `Provider`
   contract (directly or in a trusted plugin).

## Consequences

- The macOS Auth action and `anypick add account --new` can complete subscription
  login in the browser and save the result without opening the provider's native
  app UI or requiring a second AnyPick command.
- Official workflows are the safe default and easiest path, reducing accidental
  use of fragile or policy-sensitive integrations without forbidding user choice.
- Provider launchers must be installed; otherwise the method is shown as
  unavailable and the existing clear/login/save hand-off remains available.
- Login commands can take minutes, so tray mutations stay single-flight and
  expose only sanitized progress and errors.
- Full provider plugins remain trusted executable code under ADR-0012. Method
  provenance is presentation data and does not make a community plugin safe by
  itself.

## Rejected alternatives

- **Implement OAuth in core.** This would centralize vendor client secrets,
  redirect handling, scopes, and token formats and would drift from provider
  launchers.
- **Allow official methods only.** This prevents provider/plugin authors and
  users from choosing a community method even when they understand the tradeoff.
- **Run a full native app and scrape its files later.** It is slower, harder to
  automate, and unnecessary when a login-only launcher owns the same store.
- **Save only after the user runs a second command.** This preserves the old
  interruption and can leave the native store and AnyPick active pointer out of
  sync.
