# ADR-0031: Third-party providers register in-tree; no runtime registration hook

- Status: accepted (2026-10-03)
- Amends: [0015](0015-declarative-compatible-provider-plugins.md),
  [0021](0021-plugin-host-removed-from-native-builds.md)
- Provenance: `CONTRIBUTING.md` told a third-party provider author to "pass it
  to the app before services start resolving adapters", and `app.Options` has
  no such parameter. The guide documented a seam the binary never had.

## Context

ADR-0021 removed the plugin host from native builds. It decided that the
registry and the whole-package digest survive, that no third-party code is
loaded, and that ADR-0015's declarative mechanism "outlives the plugin channel:
`provider.RegisterBuiltinCompatibles` registers the same declarative definitions
in-tree at startup. Only third-party *installation* is gone."

That left one seam unrecorded, and the two halves of the tree read it
differently.

The contributor guide kept the pre-0021 shape. `CONTRIBUTING.md:131` says a
third-party provider can be "pass[ed] to the app before services start
resolving adapters". It cannot be. `app.Options` carries exactly four fields —
`Root`, `SkipMigrate`, `Bare`, `SkipMaintenance` — `app.New` takes a root string
and nothing else, and `compose()` calls `provider.RegisterBuiltin`,
`RegisterBuiltinCompatibles` and `client.RegisterBuiltin`, then stops. Registries
are sealed once startup begins, and no caller gets a window.

The code, meanwhile, carries the orphaned implementation of that hook.
`transport.AccountAdapterFor` and `transport.GenericAccountAdapter` were
introduced in `f460868 feat(go): add the Go engine that replaces the TypeScript
service` and have never had a caller, not even on the day they landed. The
TypeScript module they ported (`src/sources/account-adapters.ts`) does not
exist in the tree; ADR-0020 removed the JS toolchain. The declarative role they
would have filled is already filled in-tree by
`provider.RegisterBuiltinCompatibles`, which builds its `transport.Adapter`
inline at `internal/provider/compatible.go:114`.

ADR-0021 decision 3 preserved a seam on purpose — `proxy.PluginLifecycle` and
`internal/proxy/service_plugin.go`, "the contract a future Go-native declarative
executor would implement". These two functions are not that seam. Leaving both
readings in place means the repository asserts a feature in its contributor
guide and has no way to run it, and `CODE_REVIEW.md` §8 line 420 makes a
document naming a capability the binary lacks a P1. `checkDocumentCitations`
cannot catch this one, because `CONTRIBUTING.md` names no file for the hook — the
citation has nothing to resolve.

## Decision

1. **In-tree only.** A provider is registered in-tree, through
   `provider.RegisterBuiltin`, `provider.RegisterBuiltinCompatibles` or
   `client.RegisterBuiltin`. `app.Options` gains no registration parameter, and
   `app.New` gains no hook. A runtime registration seam is not planned and is
   not a standing extension point.
2. **The extension point for a new provider is declarative data**, not an
   executable hook: a compatible-provider definition registered in-tree, which is
   what ADR-0015 designed and what `RegisterBuiltinCompatibles` already loads.
   A provider needing behaviour beyond a declarative definition is a change to
   the engine, reviewed like any other.
3. **The preserved seam is ADR-0021's**, not these two functions.
   `proxy.PluginLifecycle` and `internal/proxy/service_plugin.go` stay, as the
   contract a future Go-native declarative executor would implement.
4. **The orphaned constructors are deleted.** `AccountAdapterFor` and
   `GenericAccountAdapter` go, with `AccountAdapterFor`'s `proxyEnabled bool`
   parameter that its body never read. The pairing-matrix rows that pinned them
   go with them.
5. **`CONTRIBUTING.md` states the mechanism that exists**: register in-tree, or
   add a declarative compatible-provider definition.

## Consequences

A contributor following the guide can actually land a provider, and the guide
cannot drift from the binary without `make check` noticing a missing
registration path rather than a contributor discovering it at runtime.

The cost is that a genuinely out-of-tree provider is not possible today, and
this ADR says so instead of leaving it implied. If that need becomes real, it
arrives as a new ADR that reopens decision 1 and restores decision 3's contract
to life — which is what decision 3 is for.

`AccountAdapterFor` and `GenericAccountAdapter` are not referenced by any
document, ADR or comment, so deleting them removes no documented capability.
The matrix loses three rows; `GenericAccountAdapter`'s direct spot-check in
`adapter_test.go` goes with it, and `internal/provider/compatible.go`'s inline
adapter keeps its own coverage.