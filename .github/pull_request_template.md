<!--
One logical change per PR. CODE_REVIEW.md is the authority for classification,
severity, evidence, and merge criteria. Complete every applicable field.
-->

## What and why

<!-- What changes, and what problem it solves. Link an issue if there is one. -->

## Review classification

<!-- Select exactly one. If uncertain, choose the higher class. -->

- [ ] **Low** — typo, comment, or documentation-only correction; no contract change
- [ ] **Normal** — bounded behavior change in a familiar area; no trust boundary
- [ ] **High** — credentials, activation, transfer, proxy/secrets, locking,
      persistence/schema, protocol, release, gate/exemption, or unfamiliar area
- [ ] **Architectural** — a decision that constrains future work; new ADR included

**Classification rationale:** <!-- Why this class, and which trigger applies? -->

## Risk and authority

**Failure impact:** <!-- What breaks if this is wrong, and how would we know? -->

**Highest-scrutiny area:** <!-- The one part of the diff that deserves most scrutiny. -->

**Authorities:** <!-- AGENTS.md invariant(s), ADR(s), contract/docs paths. -->

## Independent review

<!-- High and Architectural changes require an independent contract review and
     evidence audit. Self-review is not independent review. One independent
     reviewer may perform both roles only if they did not author or repair the
     change. Select exactly one route. -->

- [ ] **Not required** — Low or Normal change
- [ ] **Complete** — High or Architectural change

**Reviewer:** <!-- Named human or separate agent context; write N/A if not required. -->

**Reviewed head SHA:** <!-- Exact commit reviewed; write N/A if not required. -->

**Review link:** <!-- PR comment/artifact URL; write N/A if not required. -->

**Evidence auditor:** <!-- Named human or separate agent context; write N/A if not required. -->

**Evidence link:** <!-- Audit comment/artifact URL; write N/A if not required. -->

- [ ] Reviewer received the exact diff plus the authority paths
- [ ] Findings include path, authority, failure mode, and required regression
- [ ] Every P0/P1 is closed; P0/P1 was not deferred
- [ ] Every deferred P2 has a linked follow-up, owner, and due date
- [ ] Changed hunks were re-reviewed and affected check re-run after fixes

## Gate evidence

<!-- Evidence must belong to the current head SHA. Include command, exit status,
     and raw output or a CI URL. `make ci` is universal and cannot be omitted.
     For a conditional platform gate, write `not applicable — <reason>` or
     `gate not run — <reason>` instead of silently omitting it. -->

**Evidence head SHA:**

| Gate | Exit status | Raw output or CI URL |
| --- | --- | --- |
| `make ci` — review + lint + race |  |  |
| macOS: `make test-macos` |  |  |
| Tauri: full CI job — typecheck, UI build, Rust check/format/test/clippy/audit |  |  |
| Docs: `make check-docs build-docs` |  |  |

## Regression evidence

<!-- For every behavior change, show the test failing before and passing after.
     Use the real producer and assert the user-visible outcome. -->

**Red before:**

**Green after:**

## Universal checklist

- [ ] One logical change; refactoring is not bundled with unrelated behavior
- [ ] Tests are isolated — no test reaches a real `~/.anypick`
- [ ] Persisted state changes were exercised through `make sandbox`
- [ ] Error paths are asserted, not only the happy path
- [ ] Commit subjects follow Conventional Commits and read as release notes
- [ ] `CHANGELOG.md` is untouched
- [ ] User-facing behavior changes update `docs/src/content/docs/`

## If this touches credentials, activation, transfer, or proxies

<!-- Delete this section if it does not apply. -->

- [ ] Capability pairs come from `TransportFor` in `src/go/internal/transport`
- [ ] No proxy secret reaches stdout, stderr, `--json`, logs, or fixtures
- [ ] New activation behavior is expressed as `ActivationPlan.steps`
- [ ] Rollback/compensation can finish after cancellation
- [ ] New persisted mutations take `ctx` first and have no ctx-less form
- [ ] Inside a lock closure, the closure's context is used

## If this touches persistence, locking, or schema

<!-- Delete this section if it does not apply. -->

- [ ] The coordinator owns every persisted mutation (ADR 0009)
- [ ] Lock scope is provider-wide and re-entrant where ADR 0011 requires it
- [ ] A new table has exactly one owner in `internal/db/tables.go`
- [ ] Registry publication uses `internal/pubgate` correctly
- [ ] `make test-race` is green

## If this touches CLI, TUI, desktop protocol, release, or macOS

<!-- Delete this section if it does not apply. -->

- [ ] CLI/TUI prompts are TTY-gated before service access; JSON stays machine-readable
- [ ] Protocol version is synchronized across Go, Rust, and Swift
- [ ] Version changes use `make set-version` / `make bump-version`
- [ ] macOS UI follows `src/macos/README.md` and uses native controls
- [ ] Both Node-free bundle checks agree

## If a serious finding was a family, not a one-off

<!-- CODE_REVIEW.md §5: a finding that is only fixed will be paid for again. -->

- [ ] A mutation-checked invariant test or gate was added with the fix
