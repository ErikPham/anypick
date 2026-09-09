# Security Policy

## Supported versions

Security fixes are applied to the latest released minor version of AnyPick. Do not rely on unreleased commits for security patches.

## Reporting a vulnerability

Please report vulnerabilities privately to the maintainers through the repository's security advisory feature. Do not open a public issue for credential exposure, arbitrary file access, proxy authentication bypass, or unsafe account switching.

Include the affected version, a minimal reproduction, impact, and any relevant platform details. Maintainers will acknowledge reports within seven days and coordinate disclosure after a fix is available.

## Security boundaries

AnyPick intentionally handles local credentials and client configuration. It writes only to AnyPick-managed state and explicit adapter-owned paths, and journals compensating actions before mutable activation steps.

## Required branch protection

AnyPick enforces its invariants through automated gates (`make review`, and AST gates that are mutation-checked) and through human review. The gates live in the repository; the *requirement that they run before merge* does not, so it is recorded here as the verifiable source of truth. **This must be set in the repository's branch protection rule on GitHub — it cannot be expressed as a file in the tree.**

The `main` branch must be protected with:

- **Require a pull request before merging.** No direct pushes to `main`.
- **Require at least one approving review from an independent reviewer.** Self-review is not independent review; a risky change needs a reviewer who did not write it. `CODEOWNERS` (`* @outof0`) is what makes a code-owner review bite once contributors exist — it has no effect until branch protection requires code-owner review.
- **Require status checks to pass before merge.** The following CI checks must be *required*: format / vet / staticcheck (Ubuntu), Go tests (Ubuntu), Go tests (Windows), the Tauri app check, the macOS app + helper contract, and the docs site build.
- **Require the "Review metadata" job** (the job that records review metadata for architectural / severity-gated changes) to pass.
- **Do not loosen these when a new contributor joins.** The opposite: enable `CODEOWNERS` + branch protection *first*, then grant push access.

These are hosting settings. Verify them in Repository settings → Branches; they are the machine-checkable record of the review bar described in `CODE_REVIEW.md`.

## Plugins

**A native build of AnyPick does not load plugin code.** You can still install,
pin and enable a plugin, but nothing executes it: there is no plugin host in a
native release (see [ADR-0021](adr/0021-plugin-host-removed-from-native-builds.md),
which supersedes [ADR-0012](adr/0012-plugin-trust-boundary.md) and
[ADR-0014](adr/0014-plugin-package-digest.md)).

The registry half of the old design is intact and still enforced:

- AnyPick never downloads or updates a plugin. There is no network registry or auto-discovery — `plugin add` records a local directory.
- Installing does not enable. A plugin is recorded disabled until you run `anypick plugin enable <name>`.
- A SHA-256 digest of the **entire plugin package** (manifest + every shipped file, excluding VCS/dev dirs) is computed and compared on every load. If any file changes — not only `main` — the candidate is refused until you review it and run `anypick plugin trust <name>`.
- `ANYPICK_NO_PLUGINS=1` disables plugin loading entirely.

Because no entry module is ever loaded, an enabled plugin currently grants
third-party code **no** access to your credentials. Treat that as the present
state, not as a guarantee for all time: if a Go-native executor lands, enabling
a plugin will again mean trusting its author, and this section will be updated
in the same change.

**Out of scope for this build:** reports about loading without an enable,
executing before package-digest verification, or an entry path escaping the
plugin directory describe a boundary that does not exist in a native build and
cannot be reached. Do still report: digest comparison that fails to detect a
changed file, a plugin path that escapes the plugin directory on `add` or
`inspect`, or any code path that loads and runs a plugin entry module — the
last one would mean ADR-0021 has been violated.
