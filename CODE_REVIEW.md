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

1. **Automated gates** — `make fmt-check`, `make vet`, `make test`, `make gates`.
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

This is already the operating rule in `.pi/APPEND_SYSTEM.md` (auditor →
implementer → auditor → test-auditor). It applies to hand-written changes too.

---

## 2. Severity

Taken from the existing auditor agents in `.pi/agents/` — do not invent a
parallel scale.

| Level | Meaning | Merge |
| --- | --- | --- |
| **P0** | Security hole, credential/data loss or corruption, deadlock, broken published contract, a secret crossing an output boundary. | Blocked. |
| **P1** | Correctness risk on a reachable path, a bypassed invariant, a missing regression for a behaviour change, user-facing docs that now lie. | Blocked. |
| **P2** | Missing validation, confusing naming, duplication worth extracting, avoidable allocation or N+1, a test that would not fail if the code were deleted. | Fix, or open a follow-up with an owner. |
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
   edited**.
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

- [ ] `make review` — one command: `fmt-check` + `vet` + `test` + `gates`
      (paths + protocol-sync + release-check)
- [ ] `make lint` — staticcheck. CI runs it, so red lint is a red PR. It needs
      `go install honnef.co/go/tools/cmd/staticcheck@v0.8.1` locally.
- [ ] `make test-race` — for anything touching concurrency, the coordinator, or
      the proxy
- [ ] The affected platform job: `make app-test` / `make tauri-check` /
      `cd docs && pnpm build`

**Shape**

- [ ] One logical change. A refactor bundled with a behaviour change is two PRs.
- [ ] Commit subjects are Conventional Commits written for a release reader —
      they *are* the changelog (only `feat`, `fix`, `perf` appear).
- [ ] `CHANGELOG.md` untouched.
- [ ] Comments explain *why*. Cite an ADR number when the constraint is
      load-bearing. No comments narrating the current task.
- [ ] No dead code, no commented-out blocks, no `TODO` without an owner or issue.

**Tests**

- [ ] A behaviour change has a test that **fails before and passes after**.
      A test that passes with the change reverted is decoration.
- [ ] Tests are isolated: `t.TempDir()` + `app.Options{SkipMigrate: true}`, or
      `isolateEnv(t)` in `cmd/anypick`. No test may reach a developer's real
      `~/.anypick`.
- [ ] Anything that writes state was exercised through `make sandbox`.
- [ ] Error paths are asserted, not just the happy path.

---

## 5. Findings become gates

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
| **CI / make gate** | The fact is asserted in more than one language or place. | `make protocol-sync`, `make release-check`, `check-node-free-app.sh` |

Every P0 and every P1 that was not a one-off gets a rung-3 or rung-4 gate in the
same change. A P0 fixed with only a comment will be paid for again — this has
already happened: the `src/` move added a `make paths` guard and still shipped
`working-directory: go` in CI, because the guard did not check GitHub Actions
syntax. The guard was then extended to cover it. That is the pattern.

Two rules about gates themselves:

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

### Proxies and secrets

- [ ] No proxy secret reaches stdout, stderr, `--json`, a log line, or a test
      fixture. `StartResult.Token` is discarded by callers on purpose — keep it
      that way; JSON output lands in CI logs.
- [ ] Listeners bind loopback only.
- [ ] Per-instance request authentication is intact (ADR 0006).
- [ ] Nothing that prints a secret was added to a failure path.

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

- [ ] A directory move under `src/` was preceded by `make paths`.
- [ ] Desktop protocol version bumped in **all three**: `internal/buildinfo`,
      `src/tauri/app/src/protocol.rs`, `src/macos/AppServiceClient.swift`.
- [ ] A version change is applied through `make release-set` / `release-bump`;
      `VERSION` is canonical and three other files must agree.
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

| Size | Route |
| --- | --- |
| **Typo, comment, doc fix** | Universal checklist. Ship. |
| **Normal change** | Universal + area checklists. Self-review against this file, with gate output pasted into the PR. |
| **Risky change** — credentials, activation, proxy, locking, schema, protocol, release, or an area you have not touched before | Independent review required (below). Plus an ADR if it is load-bearing. |

Write an ADR when a decision will constrain future work: a new trust boundary,
a new persistence owner, a protocol change, a platform support decision. Do not
edit an accepted one — write a new record and mark the old superseded.

### Independent review

For a risky change, get a review from a context that did not write the code:

1. Hand over the diff and the authority paths (`AGENTS.md` invariants, the
   relevant ADRs, the command surface).
2. Ask for P0–P3 findings with path, authority, failure mode, and required
   regression. Forbid "looks good" and require `no actionable findings` when
   that is the truth.
3. Require the reviewer to say `gate not run` for any gate whose output is not
   in the evidence it was given. **Do not accept a claim that a gate passed.**
   Reviewers — human or agent — must treat an unverified gate as failed.
4. Apply every P0/P1 fix and add the regression. Skip speculative suggestions.
5. Re-run the affected gates. Review is not done until the output is green, not
   until someone says it is.

A reusable reviewer prompt lives in §9.

### What the author owes the reviewer

- The gate output, not a summary of it.
- The risk the change carries, stated by the author: *what breaks if I got this
  wrong, and how would we know?*
- The one thing in the diff that deserves the most scrutiny.

### Before merge

- [ ] Every gate in §4 is green, output attached.
- [ ] Every P0/P1 closed, or an open follow-up with an owner and a date.
- [ ] A P0/P1 that was a family, not a one-off, has a new gate (§5).
- [ ] Commit subjects would make sense to someone reading release notes.

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
- **Docs drift.** Run `make gates` after any structural move; treat a doc that
  names a deleted path as a P1. Two live examples were found and fixed in this
  pass: the PR template asked for `pnpm check` and pointed at a TypeScript file
  deleted with the JS toolchain, and the README documented `pnpm macos:design`
  from the deleted root `package.json`.

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
Say "gate not run" otherwise.

Also answer: does this change need a new invariant test or gate rather than
only a fix? (CODE_REVIEW.md §5)

If there is nothing actionable, say "no actionable findings". Never say only
"looks good".
```

---

## 10. Open work on this standard

Tracked so it does not evaporate:

Done:

- [x] `make review` — `fmt-check` + `vet` + `test` + `gates` in one command.
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
      `refs/` and `design/` are now anchored to the root, and `make paths`
      fails if any `.go` file under `src/go` is git-ignored or if any of the
      authority documents is untracked (§4).
- [x] **The table-ownership guard and two concurrency suites were also
      untracked** — `internal/db/tables.go` + its test (invariant #6),
      `internal/pubgate/gate_test.go` and `internal/modelcache/store_test.go`.
      On `main` those rules were asserted by nobody.
- [x] **`make paths` was dead on arrival.** A `#` comment inside the recipe's
      backslash-continuation chain commented out the rest of the target —
      including the git-ignored-source check, the authority-documents check,
      and the final `exit 1` — so `make paths` reported success no matter what.
      Found by mutation: adding `refs/` to `.gitignore` still left `make paths`
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
      (2026-09-08). Shipped as §8.2 of `SYSTEM_ARCHITECTURE_2026-09-07.md`,
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

**Per change** — the PR template. It *is* the mechanism. A change with no
completed template was not reviewed, whatever the author remembers doing.

**Monthly, or every ~50 commits — one pass, about thirty minutes:**

1. `make review` and `make lint` from a **clean checkout**, with no shell
   customisation — a fresh clone, not this working directory. Both must pass
   without anyone exporting a `PATH` first. If they do not, the gate is
   unrunnable, and §1 applies: a rule that cannot be followed decays, and takes
   the rest of this document with it. Fix the bootstrap, not the reminder.
   (This has already happened once: `go` was absent from the machine's `PATH`,
   every Go target died with a bare `command not found`, and the gate was
   quietly not run.)
2. Walk the §8 list: exemption maps, recurrence, untested packages, docs drift.
3. Re-read §10. An item open across two passes is not open work, it is accepted
   risk. Write that down, or close the item.
4. Record the result — in §10 or an issue. An unrecorded pass did not happen.
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

A pass that finds nothing is a real result. Say so, with the date.
