# ADR-0025: A global OS credential store needs a reachable test seam (CRED-01)

- Status: proposed (2026-09-18)
- Provenance: found by the review audit of 2026-09-17. Independent review
  outstanding — see Consequences.

## Context

Antigravity's credential is mirrored in two places. One is the IDE's VS Code
global-state database, which is scoped to a home directory and which tests
isolate with a temp `HOME` / `GEMINI_CONFIG_DIR`. The other is the OS credential
store, written through `github.com/zalando/go-keyring` under service `gemini`,
account `antigravity`.

The second mirror is **global**. It is the same entry the Antigravity IDE itself
reads and writes, so it is not scoped by `HOME` or `ANYPICK_HOME` — the temp
directories every test in this repository sets up do not protect it.

`internal/provider` knows this. Its `TestMain` replaces the write hook for its
own test binary, with a comment stating the rule: never mutate the developer's
real Keychain from the package test binary.

That guard did not travel. The hook was unexported, so it protected exactly one
test binary. `internal/accounts` drives the real activation path
(`svc.Use` → `restoreAntigravityKeyring` → the write hook) and could not reach
it. Running that test therefore reached the production writer, which runs

```
security -i add-generic-password -U -s gemini -a antigravity
```

`-U` replaces the item in place. The developer's own Antigravity refresh token
was overwritten with the fixture's.

The part that matters is how it was hidden. Unsandboxed, the test **passed**. It
surfaced only because a sandbox denied the Keychain write and the writer returned
exit status 161. A green suite was not evidence of safety here, and no gate in
`make check` could have said otherwise: the write is a legitimate production
behaviour on a legitimate production path. This is the class of defect §1 assigns
to independent judgment, and it is the reason that layer is not ceremony.

## Decision

1. **Export the seam.** `AntigravityKeyringPut` replaces the unexported hook, and
   `GeminiAntigravityFile` is an exported alias for the payload type so another
   package can spell its replacement's parameter type. An alias rather than a
   rename: the rename is 31 mechanical edits across 9 files, which is a lot of
   diff to buy a name.
2. **Every test binary that drives activation installs its own guard.**
   `internal/accounts` does so in `guardAntigravityKeyring`, which substitutes a
   no-op writer and restores the previous one on cleanup.
3. **The guard asserts the seam was consulted.** A test whose guard was unwired,
   or which quietly stopped exercising the mirror, fails with a message naming
   both possibilities — instead of passing because nothing happened.

## Consequences

- No test binary reaches the real credential store. The evidence is behavioural,
  not asserted: `internal/accounts` now passes in an environment with no
  Keychain at all, where before it failed with exit status 161 from `security`.
- Mutation-checked. With the guard installed but not recording the consultation,
  the test fails on the new assertion; reverted, it passes.
- `internal/provider`'s own `TestMain` guard is unchanged and still applies.
- **Composition-root seam (implemented):** `AntigravityKeyringPut` defaults to a
  no-op. `internal/app.compose` calls `EnableAntigravityKeyringWrites` only when
  `testing.Testing()` is false, so production binaries still write and every
  `go test` binary — including those that never install `guardAntigravityKeyring`
  — cannot reach the real Keychain by default. Per-binary guards (Decision §2–3)
  remain for recording that the mirror was consulted.
- **Residual, and the reason this stays proposed:** §7 puts a change to a trust
  boundary on the High-risk route, which requires an independent context. The
  author cannot supply that for their own change, so this record stays
  **proposed** until one reviews it.
