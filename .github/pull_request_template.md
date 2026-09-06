<!--
One logical change per PR. The review standard is CODE_REVIEW.md — read it if
this PR touches credentials, activation, proxies, locking, the schema, or the
desktop protocol.
-->

## What and why

<!-- What changes, and what problem it solves. Link an issue if there is one. -->

## Risk

<!-- What breaks if you got this wrong, and how would we know? -->
<!-- What in this diff deserves the most scrutiny? -->

## Independent review

<!-- CODE_REVIEW.md §7: required for a risky change — credentials, activation,
     proxy, locking, schema, protocol, release, or an area you have not touched
     before. §1: self-review is not independent review. An agent that wrote the
     change and then reported on it has produced one artifact, not two. -->

- [ ] **Not required** — typo/comment/doc fix, or a normal change in a familiar
      area
- [ ] **Done** — reviewer: <!-- a separate agent context, or a named human -->
- [ ] The reviewer was handed the diff plus the authority paths, and returned
      P0–P3 findings carrying path, authority, failure mode, and the regression
      that would have caught it
- [ ] Every P0/P1 is closed, or has a follow-up with an owner and a date
- [ ] The reviewer said `gate not run` for any gate whose output it was not
      shown — no gate was accepted on a claim alone
- [ ] Affected gates were re-run after the fixes; output is attached below

## Gates

<!-- Paste output, do not summarise it. Delete a line only if it cannot apply. -->

- [ ] `make fmt-check`
- [ ] `make vet`
- [ ] `make test`
- [ ] `make gates`
- [ ] `make test-race` — concurrency, coordinator, or proxy
- [ ] Platform: `make app-test` / `make tauri-check` / `cd docs && pnpm build`

## Checklist

- [ ] Tests added or updated; each one fails before and passes after
- [ ] Tests are isolated — no test reaches a real `~/.anypick`
- [ ] Commit subjects follow Conventional Commits; they are what release notes
      are generated from (see CONTRIBUTING.md)
- [ ] `CHANGELOG.md` untouched
- [ ] Docs under `docs/src/content/docs/` updated if user-facing behavior changed

## If this touches credentials, activation, or proxies

<!-- Delete this section if it does not apply. -->

- [ ] Any capability pairing is backed by `TransportFor` in
      `src/go/internal/transport` — never inferred from `internal/client`, which
      lists what exists, not what is allowed
- [ ] No proxy secret can reach stdout, stderr, or `--json` output
- [ ] New activation behavior is expressed as `ActivationPlan.steps`, so executor
      and rollback stay in sync
- [ ] New persisted mutations take `ctx` first and have no ctx-less form
      (ADR 0009); inside a lock closure the closure's `c` is used, not the outer
      `ctx`
- [ ] An ADR is added for a load-bearing decision (accepted ADRs are superseded,
      never edited)

## If this touches the desktop protocol or release

- [ ] Protocol version bumped in all three places: `internal/buildinfo`,
      `src/tauri/app/src/protocol.rs`,
      `src/macos/AppServiceClient.swift`
- [ ] `make release-set` / `make release-bump` used for any version change

## If this touches the macOS app or the bundle

- [ ] Native controls, System Settings structure (see `src/macos/README.md`)
- [ ] Bundle stays Node-free — both `check-node-free-app.sh` and
      `anypick-artifact node-free` agree

## If a review finding was a family, not a one-off

<!-- CODE_REVIEW.md §5: a finding that is only fixed will be paid for again. -->

- [ ] A new invariant test or gate was added alongside the fix

