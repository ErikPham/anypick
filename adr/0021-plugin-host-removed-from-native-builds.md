# ADR-0021: Native builds install plugins but never execute them (EXT-05)

- Status: accepted (2026-08-29)
- Supersedes: [0012](0012-plugin-trust-boundary.md), [0014](0014-plugin-package-digest.md)
- Amends: [0015](0015-declarative-compatible-provider-plugins.md)

## Context

ADR-0012 and ADR-0014 designed a plugin system around one mechanism: a
third-party ESM package is installed disabled, pinned to a whole-package
SHA-256, and `import()`ed into the AnyPick process only after an explicit
enable. The enable decision was therefore a *security boundary*, and
`SECURITY.md` documented it as one.

ADR-0020 §5 then removed the Node runtime from native builds. Native downloads
are one Go engine with no second runtime, and executable ESM plugin packages
are not a native-release requirement. The host that used to run them
(`internal/pluginhost`, a supervised private Node process over stdio RPC) is
gone.

What that left behind is a gap between the documentation and the binary:

- `SECURITY.md` still said plugins "run inside the AnyPick process" and invited
  reports for "executing before package-digest verification" — a boundary that
  no longer exists.
- `CONTRIBUTING.md` and `docs/src/content/docs/guides/plugins.mdx` still taught
  `export default { activate(ctx) { … } }`. A reader following that guide today
  installs a plugin, enables it, and nothing happens: every candidate degrades
  to a `LoadFailure` carrying `ErrPluginHostRemoved`.
- ADR-0012 and ADR-0014 remained marked `accepted`, so the record set implied a
  capability the shipped binary does not have.

For an open-source project this is worse than a missing feature. `SECURITY.md`
is a trust document, and a plugin guide that silently does nothing is a support
burden that lands on the maintainer.

## Decision

1. **The registry stays.** `internal/plugins` keeps owning manifests,
   whole-package digests, enable/disable and add/remove. `anypick plugin …`
   keeps working, and the ADR-0014 digest is still computed and still compared
   on every load, so tampering with an installed package is still detected and
   still refuses the candidate.
2. **Nothing is executed.** There is no plugin host, and no code path can load
   a third-party entry module. Every candidate degrades to a `LoadFailure` whose
   reason names this removal, so `doctor`, `plugin list` and the desktop
   protocol stay authoritative instead of reporting a healthy empty plugin set.
   The degraded state is `inactive`, not `degraded`, and it is never fatal.
3. **The seam is kept deliberately.** `proxy.PluginLifecycle` and
   `internal/proxy/service_plugin.go` survive even though no provider currently
   implements `provider.ProxyStarter`. They are the contract a future Go-native
   declarative executor would implement. When it lands it replaces
   `activatePlugins`; the registry, the CLI and the proxy contract do not change.
4. **Declarative compatible providers ship as built-ins.** ADR-0015's mechanism
   outlives the plugin channel: `provider.RegisterBuiltinCompatibles` registers
   the same declarative definitions in-tree at startup. Only third-party
   *installation* is gone. ADR-0016 (capability routing) is unaffected — it was
   always about routing, not about how a provider arrived.
5. **Documentation states the current behaviour.** `SECURITY.md` says plainly
   that no plugin is loaded in a native build and that the plugin boundary is
   therefore not an attack surface today. The authoring guides are removed or
   marked, not left to imply a capability that does not exist.

## Consequences

**Easier:**

- The attack surface shrinks to zero for third-party code: a native build
  cannot execute plugin code, so the entire class of "malicious enable" reports
  ADR-0012 invited is unreachable.
- `SECURITY.md` stops describing a boundary that no longer exists, so security
  reports are about the code that actually ships.
- No Node runtime in any artifact (ADR-0020) now extends to plugins as well,
  and the two ADRs agree instead of contradicting each other.

**Harder:**

- Extending AnyPick with a private provider now requires a fork, which is
  exactly the problem ADR-0012 set out to solve. That problem is real and is
  not solved by this record; it is deferred, not dismissed.
- The gap between "installed and enabled" and "does nothing" must stay loud.
  If the `LoadFailure` reason is ever softened to a silent empty list, users
  will reasonably conclude the plugin loaded. The failure is the feature.
- Two descriptions of the world exist in the record set. A reader must now
  follow 0012 → 0021 to learn the current state. That cost is accepted: editing
  0012 to say something different from what it decided would break the property
  that makes ADRs worth citing.

**Follow-up:** a Go-native executor would restore third-party extensibility
without a second runtime. It should be proposed as its own ADR, and it must
reuse the registry and digest rather than replace them — the trust model in
0012/0014 is still the right shape, it just has nothing to gate today.
