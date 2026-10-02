# Code review standard

AnyPick rewrites other tools' credential files and spawns local proxies. A bad
merge does not produce a bug report — it destroys a developer's real logins, on
their machine, with no way for us to see it. That is why the bar is what it is.

This document is the standard a change is reviewed against. It does not repeat
`AGENTS.md` (invariants, traps) or `CONTRIBUTING.md` (mechanics, commits). It
says how a change is judged, by whom, and what happens to what review finds.

---

## 1. The premise: who reviews what

Read this before copying a review process from a larger project.

| Fact | Consequence |
| --- | --- |
| The history has one human author. | "Two approvals" and reviewer rotations are unavailable. A rule that cannot be followed decays, and takes the rest of this document with it. |
| Most code is produced by AI agents. | The reviewable unit is an agent's output, and an agent's self-report is not evidence. |
| Findings keep recurring in families (stale paths, ctx-less mutations, docs describing a system that no longer exists). | A finding that is only fixed is a finding that will be paid for again. |

So review happens in **three layers**, in this order. Cheapest first; judgment
last, and only on what machines cannot decide.

1. **Automated check** — `make ci` (`review` + `lint` + `test-race`).
   Non-negotiable, no discretion, no exceptions.
2. **Invariant tests** — the project's real safety net. A rule that matters is a
   test that fails the build (§5).
3. **Judgment review** — a human or a *separate* agent context, against the
   checklists in §6–§7, for the parts a machine cannot see.

The corollary is the single most important rule in this document:

> **Self-review is not independent review.** An agent that wrote a change and
> then reported on it has produced one artifact, not two. If a change is risky
> enough to need a second opinion, that opinion must come from a context that
> did not write the code.

The operating rule here is **auditor → implementer → auditor → test-auditor**:
an independent context audits the change, the author implements it, the audit is
repeated on the result, and a further context audits the tests. It applies to
hand-written changes too.

This rule used to live only in `.pi/APPEND_SYSTEM.md`, which `.gitignore`
excludes — so a clean clone, CI, and every agent session starting from `main`
were reviewing against a rule they could not read. The copy there had also gone
stale: it named pre-`src/` paths and still called TypeScript the behavioural
authority, which ADR 0020 replaced. A rule that lives in the standard is
followed; a rule in an ignored file is not.

---

## 2. Severity

Taken from the existing auditor agents in `.pi/agents/` — do not invent a
parallel scale.

| Level | Meaning | Merge |
| --- | --- | --- |
| **P0** | Security hole, credential/data loss or corruption, deadlock, broken published contract, a secret crossing an output boundary. | Blocked. |
| **P1** | Correctness risk on a reachable path, a bypassed invariant, a missing regression for a behaviour change, user-facing docs that now lie. | Blocked. |
| **P2** | Missing validation, confusing naming, duplication worth extracting, avoidable allocation or N+1, a test that would not fail if the code were deleted. | Fix, or open a follow-up with an owner and a due date. |
| **P3** | Opinion, style a linter does not cover, future refactoring. | Say it once, then drop it. |

A finding must carry **path, symbol or tight line range, the authority it
violates, the concrete failure mode, and the regression test that would have
caught it.** "Consider using X" with no failure mode is a P3, whatever it feels
like. When there is nothing to say, say `no actionable findings` — never just
"looks good", which is indistinguishable from not having read it.

---

## 3. Authority and precedence

When two documents disagree, this is the order. Reviewers cite the authority a
change violates, not their taste.

1. `AGENTS.md` invariants — the highest-precedence statement of what must hold.
2. Accepted ADRs by number (`adr/`). Accepted records are **superseded, never
   edited**. Their decisions remain authoritative, but filenames, language, and
   test names from before the Go cutover are historical implementation detail;
   use `AGENTS.md` and the current composition root for today's symbols.
3. The code itself, for behaviour not covered by an ADR — `src/go/internal/app/app.go`
   is the composition root.
4. `docs/src/content/docs/` and `README.md` — what users believe. If a change
   makes these wrong, the change is incomplete.
5. `docs/history/` — **provenance only.** Superseded proposals. Never cite it as
   spec.
6. `src/macos/README.md` for macOS UI contract; `design/` boards are IA and
   copy, not pixel specs.

---

## 4. The universal checklist

Every change, including one-line ones.

**Gates (must be green — paste the output, don't claim it)**

- [ ] `make ci` — one command for the universal Go engine checks: `make review`
      (`fmt-check` + `vet` + `test` + `check`), `make lint`, and `make test-race`.
      Use the same pinned staticcheck version as CI.
- [ ] The affected platform job: `make test-macos`; the complete Tauri CI job
      (typecheck + UI build + Rust check/format/test/clippy/audit); or
      `make check-docs build-docs`

**Shape**

- [ ] One logical change. A refactor bundled with a behaviour change is two PRs.
- [ ] Commit subjects are Conventional Commits written for a release reader —
      they *are* the changelog (only `feat`, `fix`, `perf` appear).
- [ ] `CHANGELOG.md` untouched.
- [ ] Comments explain *why*. Cite an ADR number when the constraint is
      load-bearing. No comments narrating the current task.
- [ ] No dead code, no commented-out blocks, no `TODO` without an owner or issue.

**Commit record** — the commit is the reviewable unit. GitHub Actions
(`.github/workflows/ci.yml`, `release.yml`) run on `main` and on pull
requests; `SECURITY.md` requires branch protection so those jobs are
mandatory before merge. The commit trailer is the durable record of the
risk class and the review (§12).

- [ ] Install the gate once per clone: `make hooks`. An uninstalled gate has
      caught nothing, and reads exactly like a passing one.
- [ ] Exactly one `Risk:` trailer — `Low`, `Normal`, `High` or `Architectural`
      (§7) — **in the message's final paragraph**. Above a closing paragraph it
      is body prose: the gate once accepted that while
      `git log --format='%(trailers:key=Risk)'` returned nothing, so the
      classification existed only in the author's head.
- [ ] `Risk: High` and `Risk: Architectural` carry `Reviewed-By:` naming a
      context that is not the author; Architectural also carries `ADR:`.
- [ ] `fixup!`/`squash!` subjects are exempt because `--autosquash` rewrites
      them away. A `Revert "..."` subject is not: a revert lands, so give it a
      `revert:` subject (`git revert --edit`).

**Tests**

- [ ] A behaviour change has a test that **fails before and passes after**.
      A test that passes with the change reverted is decoration.
- [ ] Tests are isolated: `t.TempDir()` + `app.Options{SkipMigrate: true}`, or
      `isolateEnv(t)` in `cmd/anypick`. No test may reach a developer's real
      `~/.anypick`.
- [ ] Anything that writes state was exercised through `make sandbox`.
- [ ] Error paths are asserted, not just the happy path.

---

## 5. Findings become check

This is the mechanism that makes review compound instead of repeating. It is
already how this codebase works — `internal/store/coordinator_test.go` parses
the AST to fail the build when a mutating method bypasses the coordinator;
`internal/db/tables_test.go` does the same for table ownership.

**If a rule is worth reviewing for, it is worth encoding.** Use this ladder and
stop at the lowest rung that actually holds:

| Rung | Use when | Example in this repo |
| --- | --- | --- |
| **Comment** | The constraint is local and obvious from the code. | `// re-entrancy is carried in the context (ADR 0009)` |
| **Checklist item** | Judgment is genuinely required every time. | "does the plan express rollback?" |
| **Test that fails the build** | The rule is structural, mechanical, and silently violable. | `coordinator_test.go`, `tables_test.go`, `help_parity_test.go` + `testdata/parity/cli-tree.json` |
| **CI / make gate** | The fact is asserted in more than one language or place. | `make check-protocol`, `make check-release`, `check-node-free-app.sh` |

Every P0 and every P1 that was not a one-off gets a rung-3 or rung-4 gate in the
same change. A P0 fixed with only a comment will be paid for again — this has
already happened: the `src/` move added a `make check-paths` guard and still shipped
`working-directory: go` in CI, because the guard did not check GitHub Actions
syntax. The guard was then extended to cover it. That is the pattern.

Two rules about check themselves:

- **Two implementations of the same gate must reject the same shapes.** The
  Node-free bundle check exists twice — `src/macos/scripts/check-node-free-app.sh`
  (Xcode) and `go run ./cmd/anypick-artifact node-free <root>` (CI). If you
  tighten one, tighten both, and prove it with a deliberately bad fixture.
- **Never weaken a gate to make a change pass.** Deleting a test, loosening a
  fixture, or adding an escape-hatch entry without a written reason is a P0 on
  the reviewer, not on the author.

---

## 6. Area checklists

Apply the universal list, plus the list for every area the diff touches.

### Credentials, activation, transfer

- [ ] Capability pairs come from `TransportFor` in `internal/transport`. Never
      inferred from `internal/client`, which lists what *exists*, not what is
      *allowed*.
- [ ] New activation behaviour is expressed as `ActivationPlan.steps`, so
      planner and executor cannot diverge. The executor must not re-derive
      behaviour from transport capability.
- [ ] Rollback and compensation paths use `context.WithoutCancel(ctx)` — they
      must be able to finish after the triggering operation is cancelled.
- [ ] `writeMeta` remains the atomic commit point (ADR 0004). A failed backup or
      import cannot destroy the previously saved account.
- [ ] No `switch (providerId)` in core, CLI or TUI. Provider policy lives on the
      provider (`roleDefaults`, `suggestModels`, `roleFriendlyModels`,
      `staticFallbackModels`, `roleModelHints`).
- [ ] New provider behaviour was exercised against a throwaway root
      (`make sandbox`), never a real account.
- [ ] A new proxy-capable provider (or a new upstream capability) has a row in
      the discovery matrix (`internal/provider/discovery_matrix_test.go`): its
      resolved upstream publishes a catalog operation, or the exception names
      its reason. An unclassified provider fails the build.

### Proxies and secrets

- [ ] No proxy secret reaches stdout, stderr, `--json`, a log line, or a test
      fixture. `StartResult.Token` is discarded by callers on purpose — keep it
      that way; JSON output lands in CI logs.
- [ ] Listeners bind loopback only.
- [ ] Per-instance request authentication is intact (ADR 0006).
- [ ] Nothing that prints a secret was added to a failure path.
- [ ] A new `Upstream` field has a row in `upstreamCarry`
      (`internal/proxy/upstream_carry_test.go`) saying which hops carry it
      (`buildServer`, `upstreamValue`, `flattenedNativeUpstream`). An
      unclassified field fails the build; a dropped copy fails silently in
      production, never in tests.
- [ ] A vendor strictness the proxy papers over (rejected parameters, required
      schema keys) has a strict-mock case that validates like the vendor, fed
      with a realistic client-shaped body — not a mock that accepts everything.
- [ ] Hub references stay integral: deleting an account cascades its hub
      membership at the database level (`proxy_hub_sources` foreign keys) and
      prunes its route pins by value (`PruneAccount`). Positional pins and
      slice surgery are retired — a dangling `provider/account` must be
      impossible, not just rare.
- [ ] A restarted or adopted hub child serves the stored config revision
      (`hub.ChildSynced`, ADR-0030). Liveness checks alone re-adopt stale
      routing.

### Stores, locking, database

- [ ] Every persisted mutation runs under the coordinator (ADR 0009). New
      mutating store methods take `ctx` first and have no ctx-less form — the
      compiler, not the reviewer, is the gate.
- [ ] Inside a lock closure, use the closure's `c`, never the outer `ctx`.
      A fresh `context.Background()` inside a held scope is a hang, not a miss.
- [ ] Lock scope is `refs.ProviderScope`, not `refs.Serialize`. The precise
      identity silently splits one scope into many and reintroduces the
      interleaving ADR 0011 closed.
- [ ] A new table has exactly one owning package, declared in
      `internal/db/tables.go`. Services never reach past a store into SQL.
- [ ] Registries are read after they are sealed; nothing mutates the graph
      mid-operation. `internal/pubgate` is the shared publication lock — readers
      take `RLock`, a replacement commit holds `Lock` across identity + swaps +
      rollback.
- [ ] `make test-race` is green.

### CLI and TUI

- [ ] No prompt without a TTY. The guard is `isatty.IsTerminal(fd)` —
      `Mode()&os.ModeCharDevice != 0` is true for `/dev/null` and hangs under
      cron and CI.
- [ ] The TTY guard runs **before** any service is touched.
- [ ] Every command keeps `--json` machine-readable; exit codes come from
      `anyerr.ExitCode`.
- [ ] Error suggestions are attached to the error via `anyerr.WithSuggestions`,
      not formatted into a string before printing.
- [ ] Source is never guessed.
- [ ] `cmd/anypick/help_parity_test.go` passes, or `testdata/parity/cli-tree.json`
      was regenerated deliberately in the same change.
- [ ] A test that spawns a child process reports the child's stderr on every
      failure path, including a timeout. "Timeout waiting for frame" with no
      child output is unfalsifiable — it reads like a protocol bug when it is
      usually a startup failure.
- [ ] A deadline on a spawned child is a documented constant, not a bare `8 *`
      in the select. Eight seconds is enough on an idle machine and not enough
      under `go test ./...`, which makes the suite flake for reasons unrelated
      to what it asserts. The bound must still exist — a hung child must fail,
      not hang CI.

### macOS app (`src/macos/`)

- [ ] Native controls only, System Settings structure — `Form` (.grouped),
      `List` (.sidebar), `Section`, `Toggle`, `Picker`, platform fonts and
      spacing. No cards, gradients, chip strips, RGB tokens, or CSS padding.
      If SwiftUI cannot do it, bridge AppKit — do not fake the control.
- [ ] `Settings` is a `Settings` scene, not a sidebar destination.
- [ ] The menu-bar extra stays `MenuBarMenu.swift` (Pencil node `m0s8el`) with
      complete start/stop/restart actions.
- [ ] The bundle stays Node-free: both `check-node-free-app.sh` and
      `anypick-artifact node-free` agree.
- [ ] If a string-scan test fights `src/macos/README.md`, the test is wrong —
      fix the test.

### Cross-language and release

- [ ] A directory move under `src/` was preceded by `make check-paths`.
- [ ] Desktop protocol version bumped in **all four**: `internal/buildinfo`,
      `src/tauri/app/src/protocol.rs`, `src/macos/AppServiceClient.swift`,
      `src/tauri/ui/lib/wire.ts`.
- [ ] A version change is applied through `make set-version` / `bump-version`;
      `VERSION` is canonical and the five secondary sources in
      `src/go/cmd/anypick-release/version.go` (`secondarySources`) must agree.
- [ ] No support is claimed for a platform the release matrix does not build.

### Docs

- [ ] `cd docs` before any command for the site — it has its own lockfile, and
      your shell's cwd does not persist.
- [ ] User-facing behaviour changed → `docs/src/content/docs/` updated in the
      same PR.
- [ ] No capability pairing is published without reading the adapter (invariant 1).
- [ ] No doc change resurrects a removed system. Docs describing a system that
      no longer exists is a P1 — it has happened here (a plugin host that could
      not run, and a `SECURITY.md` inviting reports for an unreachable boundary).

---

## 7. Process

### Change sizes

| Class | Trigger | Route |
| --- | --- | --- |
| **Low** | Typo, comment, or documentation-only correction with no contract change. | Universal checklist. |
| **Normal** | Bounded behaviour change in a familiar area outside a trust boundary. | Universal + every affected area checklist; self-review is sufficient. |
| **High** | Credentials, activation, transfer, proxy/secrets, locking, persistence/schema, protocol, release, a gate/exemption, or an unfamiliar area. | Independent contract review and independent evidence audit are required. One reviewer may do both only if they did not author or repair the change. |
| **Architectural** | A new trust boundary, persistence owner, protocol/platform commitment, or another decision that constrains future work. | High-risk route plus a new ADR. |

The author must select exactly one class in the PR and state why. When uncertain,
choose the higher class. Review can raise but never lower the class without a
written rationale.

Write an ADR when a decision will constrain future work. Do not edit an accepted
one — write a new record and mark the old superseded.

### Roles

- **Author** — selects the risk class, names the authorities and failure impact,
  supplies tests and raw gate evidence, and never approves their own work.
- **Independent contract reviewer** — reads the diff against the named
  authorities and reports P0–P3 findings. This context did not author the change.
- **Independent evidence auditor** — checks that the recorded commands ran on
  the reviewed commit and that the outputs support the claim. This context does
  not repair the change it audits.

Low and normal changes need the author role. High and architectural changes need
all three roles. One independent reviewer may perform both independent roles if
they did not author or repair the change.

### Independent review

For a high or architectural change:

1. Record the head commit SHA. Hand over that exact diff and the authority paths
   (`AGENTS.md` invariants, the relevant ADRs, and the command surface).
2. Ask for P0–P3 findings with path, authority, failure mode, and required
   regression. Forbid "looks good" and require `no actionable findings` when
   that is the truth.
3. Require the reviewer to say `gate not run` for any gate whose output is not
   in the evidence it was given. **Do not accept a claim that a gate passed.**
   Reviewers — human or agent — treat the universal `make ci` as failed when it
   is unverified; they classify an unverified platform gate explicitly instead
   of silently accepting it.
4. Apply every P0/P1 fix and add the regression. Only P2 may be deferred, with a
   linked follow-up, owner, and due date. Skip speculative suggestions.
5. If the diff changed after review, record the new head SHA, re-run affected
   check, and re-review the changed hunks. Review is not done until the reviewed
   commit is green, not until someone says it is.

A reusable reviewer prompt lives in §9.

### Evidence packet

The PR is the durable record. It must contain:

- the selected risk class and rationale;
- the reviewed head SHA and a link to the independent review for high or
  architectural changes;
- the authority paths and the one part of the diff deserving most scrutiny;
- the risk stated by the author: *what breaks if I got this wrong, and how would
  we know?*;
- for every applicable gate: command, exit status, and raw output or CI run URL;
- red-before/green-after mutation evidence for every behaviour regression;
- links, owner, and due date for every deferred P2.

Evidence from an earlier SHA is stale. `make ci` is universal and may not be
omitted. A missing conditional platform command is written as `gate not run`;
it is never silently treated as not applicable.

### Before merge

- [ ] The PR records exactly one risk class and all required roles.
- [ ] The evidence packet belongs to the current reviewed head SHA.
- [ ] `make ci` is green; it is universal and cannot be omitted. Every
      conditional platform gate in §4 is green or says why it is not applicable.
- [ ] Every P0/P1 is closed. P0/P1 cannot be deferred.
- [ ] Every deferred P2 has a linked owner and due date.
- [ ] A recurring P0/P1 has a new mutation-checked gate (§5).
- [ ] Commit subjects would make sense to someone reading release notes.

### Emergency path

An emergency shortens coordination; it does not lower the evidence bar. Prefer
reverting to the last known-good release. Security incidents use the private
advisory path in `SECURITY.md`. If required CI is unavailable, restore service
with a known-good artifact and merge only after the check recover. No emergency
may bypass a P0/P1 or weaken a gate.

---

## 8. Health signals

Review quality is not measured in comments per PR. Watch these instead:

- **Gate escape hatches.** Every entry in `coordinator_test.go`'s exemption map
  and in the shared-table map carries a written reason. A growing list means an
  invariant is being negotiated away, not enforced.
- **Recurrence.** The same finding twice means the wrong rung was chosen (§5).
- **Untested surface.** Packages with no test files are where a regression
  hides longest — a concurrency primitive with zero tests passes review for a
  year and then costs a weekend. `internal/pubgate` and `internal/modelcache`
  were the last two; both now have suites that were mutation-checked when they
  were written (break `Clear`'s scope or the nil-`Gate` no-op and a test fails).
- **Docs drift.** Treat a doc that names a deleted path as a P1, and do not
  rely on noticing it: `checkDocumentCitations` now fails when AGENTS.md,
  CONTRIBUTING.md, CODE_REVIEW.md, SECURITY.md or an ADR cites a `*.md` that
  does not exist. It found six live ones in its first run — two accepted ADRs,
  this document and three source comments, all still pointing at plans that had
  been consolidated into `docs/history/`. It is scoped to the documents that
  state what is true now: a README has to be able to say "these were retired"
  and name them, and a source comment may cite another project's docs.
  Two earlier examples were found by hand: the PR template asked for
  `pnpm check` and pointed at a TypeScript file deleted with the JS toolchain,
  and the README documented `pnpm macos:design` from the deleted root
  `package.json`.
- **Prose that describes the code is not gated, and cannot be.** Two claims in
  AGENTS.md's own invariants had drifted from the tree: that
  `TransportFor("cline")` is `unsupported` on every account adapter — its own
  adapter returns `ManagedBuiltinProxy`, and a passing test pins that — and
  that `docs/AGENTS.md` is a symlink, which it never was. Both were corrected
  by hand. A claim about behaviour is not a citation, so there is no rung-3
  form for it; it is a §6 checklist item, and these two are the evidence that
  someone has to actually read the invariants against the code.

---

## 9. Reviewer prompt

Paste this into a fresh agent context (or hand it to a human) with the diff.

```text
You are reviewing a change to AnyPick, a Go tool that rewrites other tools'
credential files and runs local proxies. A merge mistake destroys a developer's
real logins on their own machine.

Read completely first: AGENTS.md (invariants and traps), CONTRIBUTING.md,
CODE_REVIEW.md, and every ADR the diff touches.

You do not have the diff's author's context. Do not assume the change is
correct; do not assume it is wrong. Bash is read-only: no formatters, builds,
tests, or commands that modify source, caches, or credentials.

Return findings as P0 (security, data loss, deadlock, broken contract, leaked
secret), P1 (reachable correctness risk, bypassed invariant, missing
regression, docs that now lie), P2 (validation, naming, duplication, waste),
P3 (opinion).

Every finding needs: path + tight line range or symbol, the authority it
violates (AGENTS.md invariant number, ADR number, or contract), the concrete
failure mode, and the regression test that would have caught it. A finding
without a failure mode is P3.

Do not claim a gate passed unless its output is in the evidence you were given.
Treat missing `make ci` evidence as failed. Say "gate not run" for any missing
platform evidence and state whether it applies.

Also answer: does this change need a new invariant test or gate rather than
only a fix? (CODE_REVIEW.md §5)

If there is nothing actionable, say "no actionable findings". Never say only
"looks good".
```

---

## 10. Open work on this standard

Tracked so it does not evaporate:

Done:

- [x] `make review` — `fmt-check` + `vet` + `test` + `check` in one command.
- [x] `make lint` — staticcheck, pinned at `v0.8.1` in CI. Fails when the tool
      is absent rather than skipping, and prefers the GOPATH the Makefile pins
      (the ambient GOPATH's staticcheck was built with an older Go and cannot
      analyse a module that requires go1.26).
- [x] Tests for `internal/pubgate` and `internal/modelcache`, both
      mutation-checked.
- [x] `internal/provider/workbuddy.go` is gofmt-clean; `AGENTS.md` says six CI
      jobs; the PR template and the README no longer name deleted tooling.
- [x] `src/tauri/README.md` rewritten against verified facts (it described the
      pre-ADR-0020 world: a Node supervisor, `scripts/build-tauri-tray.mjs`,
      `src/tray/**`, and "Windows remains unsupported").
- [x] **`internal/bindings` no longer has a ctx-less form of anything.** Every
      wrapper is gone — mutators *and* reads — so no method on `bindings.Store`
      can be reached through a `context.Background()` the caller did not hand
      in. Two AST guards in `internal/bindings/locking_test.go` fail the build
      if one comes back: `TestMutatingMethodsTakeContext` (a mutator must take
      `ctx` first) and `TestNoBackgroundContextInProductionCode` (no
      `context.Background()` in non-test files). Both mutation-checked:
      reintroducing a ctx-less read trips the second, a ctx-less mutator trips
      both. The guard has **zero exemptions**, deliberately — an exempted guard
      is a guard that rots, because every later `context.Background()` has to be
      judged against an exception nobody remembers. That meant paying ~15
      mechanical test call sites instead.
- [x] **Cross-process desktop tests were flaking and were undiagnosable.**
      `TestDesktopServiceCrossProcessHandshake` failed under `go test ./...`
      with `timeout waiting for service hello frame` and no child output; it
      passes in isolation. Two defects, both fixed: an undocumented 8-second
      deadline on a cold subprocess start, and a failure path that dropped the
      child's stderr. `awaitServiceFrame` now takes the `*serviceProc` (which
      owns stderr) and routes every failure through `p.fatalf`; the two
      copy-pasted 35-line spawn blocks collapsed into `spawnDesktopService`,
      which also dumps stderr from `t.Cleanup` when the test fails. Verified by
      mutation: a child that says `boom` and stays silent now reports
      `timeout after 1s waiting for service hello frame` *plus* `boom`.
- [x] `anypick reset` no longer reports success when the binding delete failed
      (`cmd/anypick/bindings.go` dropped `DeleteGlobal`'s error). Found while
      fixing the ctx bug next to it — the usual way live bugs surface.
- [x] **`CODE_REVIEW.md` was never committed.** It existed only in the working
      tree, so a clean checkout, CI, and every agent starting from `main` were
      reviewing against a standard they could not read — while `AGENTS.md`,
      `CONTRIBUTING.md` and the PR template pointed at a file that was not
      there. Landed as its own commit rather than bundled into the 100+ file
      worktree it was sitting in.
- [x] **`.gitignore` was swallowing `src/go/internal/refs`.** The `refs/` rule
      was unanchored, so git matched it at any depth and excluded a real Go
      package that 45 committed files import. Three separate silences at once:
      `main` did not compile, `git status` said nothing (an ignored path is
      invisible, not merely untracked), and `refs.ProviderScope` — the lock
      scope behind ADR 0011 and invariant #5 — was not in the repository. Both
      `refs/` and `design/` are now anchored to the root, and `make check-paths`
      fails if any `.go` file under `src/go` is git-ignored or if any of the
      authority documents is untracked (§4).
- [x] **The table-ownership guard and two concurrency suites were also
      untracked** — `internal/db/tables.go` + its test (invariant #6),
      `internal/pubgate/gate_test.go` and `internal/modelcache/store_test.go`.
      On `main` those rules were asserted by nobody.
- [x] **`make check-paths` was dead on arrival.** A `#` comment inside the recipe's
      backslash-continuation chain commented out the rest of the target —
      including the git-ignored-source check, the authority-documents check,
      and the final `exit 1` — so `make check-paths` reported success no matter what.
      Found by mutation: adding `refs/` to `.gitignore` still left `make check-paths`
      green. Every explanatory comment was moved above the target (the Makefile
      "path integrity" block documents the trap). The same pass added the
      "no build artifact is tracked" check the committed 16.9 MB
      `src/go/anypick` Mach-O exposed: once a file is tracked, `.gitignore` no
      longer applies to it, so the gate detects a tracked binary by diffing
      `git ls-files src/` (total) against `git grep -Il . -- src/` (text) **plus**
      empty files — rather than naming artifacts, which is how the binary got
      committed in the first place (the old pattern was `go/anypick`, written
      before commit 44e5046 moved everything under `src/`). Verified red by
      force-adding a tiny binary (`FAIL … tracked binary(ies)` + `path check
      FAILED`, exit 1) and green on a clean tree. The stale pre-move `.gitignore`
      entries (`go/anypick`, `go/go/`, …) were removed and `/src/go/anypick` is
      now anchored, so the release artifact can never be committed again.
      2026-09-05: the diff grew a false positive — the eight tracked AppIcon
      assets (PNG/`.icns`, which Xcode requires tracked) failed as "binaries".
      The check now excuses only `*.png`/`*.icns` whose magic bytes agree (PNG
      signature / `icns`), verified red by staging a real binary *and* Mach-O
      bytes in a `.png` name (both listed, both rejected — extension alone
      smuggles nothing). Two bash-3.2 traps documented in the Makefile block:
      no `case` inside `$(…)` (its parser ends the substitution at the
      pattern's `)`), and explanatory comments stay above the target.

Open:

- [x] **`internal/store` no longer has a ctx-less read wrapper**
      (2026-09-08). Shipped as §8.2 of the architecture plan that has since been
      consolidated into `docs/history/ARCHITECTURE_AND_ROADMAP_2026-09-08.md`,
      but this list was never updated, so it went on claiming six of them at
      `account_store.go:91,114,439,473,597` and `proxy_config.go:14`. Verified,
      not assumed: production files under `internal/store` contain **zero**
      `context.Background()`/`context.TODO()`, and no `*AccountStore` method
      manufactures one. Three methods still take no `ctx` — `rowToAccount`
      (a pure row mapper), `PrepareSnapshot` and `DiscardPreparedSnapshot`
      (filesystem staging) — and none of them builds a context. Stale line
      numbers are the reason this read as open for a day: a debt list that
      cites lines drifts the moment anyone edits the file.
- [x] **`internal/bindings` takes the coordinator lock** (2026-09-08,
      ADR-0022, accepted 2026-09-07). `bindings.Store` gained a `Root` and
      `Store.mutate` now calls `lock.WithMutationLock(ctx, s.Root, scope, fn)`
      (`bindings.go:80`), so correctness no longer rests on the caller
      remembering. Same drift as the entry above: done in code, still open on
      paper.
- [x] `.github/CODEOWNERS` exists (2026-09-07). Added for the credential
      boundary; the "wait for a second human" condition was met by making the
      boundary reviewable rather than by headcount.
- [x] PR metadata is validated by `.github/review-metadata.sh` in the
      `Review metadata` workflow: exactly one class/route, path-forced high-risk
      classification, current reviewed and evidence SHA, required independent
      review fields, platform evidence, and successful `make ci` evidence. The
      workflow runs on `pull_request_target`, checks out only the trusted base
      revision, and re-runs when the PR body is edited.
- [ ] Require the `Review metadata` job and the build/test jobs in a GitHub
      branch ruleset. Repository files cannot enforce the hosting setting;
      record the ruleset configuration in the next health issue.

---

## 11. Cadence

§8 names the health signals. Nothing schedules anyone to read them, which is
how a signal becomes a surprise instead of a decision. Two rhythms:

**Per change** — the commit record (§4). Where a repository uses pull requests
that is the PR template; here every commit goes to `main`, so it is the
`Risk:` / `Reviewed-By:` / `ADR:` trailers plus the gate that enforces them
(§12). Either way the record *is* the mechanism: a change with no record was not
reviewed, whatever the author remembers doing.

**Monthly, or every ~50 commits — one pass, about thirty minutes:**

The maintainer who cuts releases owns this pass. Record each result in one
GitHub issue labelled `review-health`; §10 remains the durable backlog, not the
run log. The issue title is `Review health — YYYY-MM-DD` and records these
metrics: risky PR review coverage, merged P0/P1 count, repeated finding families
without check, exemption count, packages without tests, docs drift, and overdue
P2 follow-ups.

1. `make ci` from a **clean checkout**, with no shell customisation — a fresh
   clone, not this working directory. It must pass
   without anyone exporting a `PATH` first. If they do not, the gate is
   unrunnable, and §1 applies: a rule that cannot be followed decays, and takes
   the rest of this document with it. Fix the bootstrap, not the reminder.
   (This has already happened once: `go` was absent from the machine's `PATH`,
   every Go target died with a bare `command not found`, and the gate was
   quietly not run.)
2. Walk the §8 list: exemption maps, recurrence, untested packages, docs drift.
3. Re-read §10. An item open across two passes is not open work, it is accepted
   risk. Write that down, or close the item.
4. Record the metrics and result in the `review-health` issue. An unrecorded
   pass did not happen.
5. Before claiming a guard "can land", prove it against `main` in a throwaway
   worktree, not in this working directory:

   ```bash
   git worktree add --detach /tmp/head-check HEAD
   cd /tmp/head-check/src/go && go build ./... && go vet ./...
   git worktree remove --force /tmp/head-check
   ```

   A guard that passes here only because the fix it depends on is sitting
   uncommitted in your working directory is a guard that will be red the moment
   it lands. This is how the three guards in §10 were found to be
   un-landable — they are not wrong, they are early.

Targets are explicit: 100% of high/architectural PRs independently reviewed;
zero merged P0/P1; zero repeated P0/P1 families without a gate; every exemption
has a written reason; no safety-critical package lacks tests; no known docs-path
drift; every P2 is closed by its due date. A pass that finds nothing is a real
result. Say so, with the date.

---

## 12. Where this standard is enforced

§1 states the order. This table says *where each layer is wired*, so a rule is
never assumed to be running because it is written down.

| Layer | Fires on | Mechanism | Catches |
| --- | --- | --- | --- |
| **L0 — commit** | every `git commit` | `.githooks/commit-msg` | subject shape, exactly one risk class, a named non-author reviewer for High/Architectural, an ADR for Architectural |
| **L0 — push** | every `git push` | `.githooks/pre-push` → `make check` | formatting, vet, and the path/parity/ownership check. The test suite is deliberately **not** duplicated here: CI runs it on the same push, and a ten-minute local gate is a gate people learn to skip (§5) |
| **L1 — CI** | a push or a pull request | `.github/workflows/` | the full suite, race detector, staticcheck, the platform builds, PR metadata validation |
| **L2 — judgment** | any change that needs it | a human, or a separate agent context (§7) | everything a machine cannot decide |

The local layers are inert until you install them, once per clone:

```bash
make hooks             # points core.hooksPath at .githooks/
sh .githooks/test.sh   # the mutation cases; a gate you have never seen fail is unproven (§5)
```

The suite's count is deliberately not written down here. It said "25" for one
afternoon while the file held 28, which is the same drift that made a debt list
cite lines that had already moved — run the command and read the number.

Three facts worth keeping in view, because each has already been true here:

- **A gate written down but not installed has caught nothing.** The hooks sat
  committed, executable and green for two days while `core.hooksPath` was unset
  — the same failure as a rule nobody can read, wearing a passing test suite.
- **L1 only executes if a pull request exists.** This repository has none: every
  commit goes to `main`. So the commit trailer, not the PR template, is the
  durable record of the risk class and the review. `make check-paths` fails if any file
  in `.githooks/` is untracked, so a clone cannot silently lose the layer that is
  actually doing the work.
- **A gate and its reader must agree on what a fact is.** `commit-msg` once
  accepted a `Risk:` line that `git log --format='%(trailers:key=Risk)'` could
  not see. Both were "working". Read trailers through
  `git interpret-trailers --parse`, never by grepping the message text.
