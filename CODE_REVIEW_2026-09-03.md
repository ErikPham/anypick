# Full-repository review — 2026-09-03

Reviewed: `main` @ `e9d8a85` (working tree clean except `src/macos/MainWindow.swift`).
Method: five independent reviewer contexts, one per risk area, against the
authority order in `CODE_REVIEW.md` §3. Every P0 below was then re-verified by
reading the source directly — none is reported on an agent's word alone.

---

## 0. Gate baseline — two of five gates are red on `main`

`make review` chains `fmt-check + vet + test + gates` and dies at step one, so
the chain never reaches the rest. I ran the remaining targets individually to
get a real baseline rather than reporting four gates as unrun.

| Gate | Result |
| --- | --- |
| `make fmt-check` | **RED** — 7 files not gofmt-clean |
| `make vet` | **green** (no output) |
| `make test` | **green** — 38/38 packages, 0 failures |
| `make gates` | **green** — paths, protocol-sync (2), release-check (1.0.0) |
| `make lint` | **RED** — 4 staticcheck findings |

**Both red gates fail exclusively in files added by `e9d8a85`**, and all eight
of that commit's quality claims in its message are about `go build` and
`go vet` — neither `gofmt` nor `staticcheck` was run before it landed.

### 0.1 `make fmt-check` — 7 files

```
internal/protocol/gemini/models.go
internal/protocol/gemini/responses.go
internal/protocol/gemini/thinking.go
internal/protocol/gemini/types.go
internal/provider/grok_credential.go
internal/provider/grok_upstream_test.go
internal/proxy/auth_provider_test.go
```

The drift is **real, not a toolchain artifact**. Confirmed across three
toolchains: go1.21.13 (`/opt/homebrew/opt/go@1.21/bin/gofmt`), go1.22.10
(`/Users/erik/sdk/go1.22.10/bin/gofmt`), and **go1.26.7** — the exact version CI
runs — via
`/Users/erik/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.7.darwin-arm64/bin/gofmt`.
All three report the identical seven files, so **CI is red on this too**, and
reformatting with the go1.26.7 binary will satisfy CI exactly.

### 0.2 `make lint` — 4 findings, all in `internal/protocol/gemini/`

```
internal/protocol/gemini/chat.go:227:6:  func appendSystem is unused (U1000)
internal/protocol/gemini/chat.go:319:6:  func stringsHasSuffix is unused (U1000)
internal/protocol/gemini/types.go:20:25: string literal contains the Unicode
                                         format character U+200B (ST1018)
internal/protocol/gemini/types.go:23:24: string literal contains the Unicode
                                         format character U+200C (ST1018)
```

The local tool is `staticcheck 2026.2.1 (0.8.1)` — the same version CI pins —
so these reproduce in CI.

The two `U1000`s are genuine dead code, and `stringsHasSuffix` is a
reimplementation of `strings.HasSuffix` that nothing calls: leftover from the
TypeScript port.

The two `ST1018`s are **intentional, not corruption.** Verified at the byte
level with `od -c`:

```go
ToolSignatureCarrier = "\342\200\213"   // U+200B ZERO WIDTH SPACE
EmptyThoughtCarrier  = "\342\200\214"   // U+200C ZERO WIDTH NON-JOINER
```

The comment above each explains it: an invisible carrier used to pass a
function-call signature through Claude's thinking block. The fix is purely
mechanical — write them as `"\u200b"` / `"\u200c"` — which is behaviourally
identical and makes the intent greppable. This is a lint failure, not a bug.

> Toolchain note: `GO_SEARCH` puts `command -v go` first, and this machine's
> `PATH` resolves `go` to `/opt/homebrew/opt/go@1.21/bin/go` (1.21.13) while
> `go.mod` requires go1.26. Formatting and these lint results are unaffected,
> but any check whose behaviour is version-sensitive is running under the wrong
> tool. `gh` is not installed, so CI's actual run history remains
> **gate not run**.

### 0.3 `make test-race` — green, and what it does *not* prove

Run separately, on the unmodified tree: **40 packages, 0 failures, no data race
reported**, including `cmd/anypick` (128.8 s) and `internal/desktop` (41.7 s).

Read this correctly. It does **not** refute findings 2.8 or 2.9 — the race
detector flags concurrently-unsynchronised memory access, and neither finding is
that. 2.8 is *lost cancellation* that becomes a deadlock only if
`internal/journal` later acquires a coordinator lock; 2.9 is an uncancellable
query plus an unlocked write, serialised by `SetMaxOpenConns(1)` and therefore
invisible to `-race`. Both stand.

What the run *does* establish is that the concurrency primitives that already
have suites — the coordinator, `pubgate`, `modelcache`, `lock` — hold under
`-race`, and that no latent data race is sitting in the newly ported gemini/grok
engines.

**Still not run:** `make app-test`, `make tauri-check`,
`cd docs && pnpm build`, and CI history (`gh` is not installed). See §7.

---

## 1. P0 — blockers

### P0-1 · Journal recovery copies the live credential file over its own backup

`src/go/internal/activation/recovery.go:118-152` (the `src=>dest` branch).

The producer, `src/go/internal/runtime/service.go:180-192`, copies **live →
backup** and records `src+"=>"+dest`, where `src` is the real file
(`~/.claude/settings.json`, `codexConfigPath`) and `dest` is under
`<root>/recovery/clients/`. The in-process undo,
`RestoreClientConfigRecovery` (`runtime/service.go:209-233`), correctly reads
`dest` and writes `src`.

`recovery.go:141-144` does the **opposite** — `os.ReadFile(src)` then
`os.WriteFile(dest, ...)` — i.e. it reads the *already-mutated* live file and
overwrites the only backup with it. It then marks the entry `rolled_back` and
increments `Recovered`.

- **Authority:** ADR 0004; `CODE_REVIEW.md` §6 "exact file rollback from
  recorded backups".
- **Failure mode:** an activation that wrote client config and then died
  (SIGKILL, health timeout, cancelled commit) leaves the journal `executing`.
  `app.go:331` runs `RecoverIncompleteOperations` on **every** bootstrap.
  Recovery then destroys the developer's original settings file, leaves the
  live file pointed at the dead proxy, and reports success. There is no second
  copy anywhere.
- **Verified:** producer and both consumers read directly. The chain is live,
  not latent: `executor.go:785` calls `deps.Runtime.PrepareClientConfigRecovery`,
  `:790` persists the records into `entry.BackupPaths`, and `app.go:331` runs
  `RecoverIncompleteOperations` on every bootstrap.
- **Regression:** feed `recover()` records produced by the **real**
  `runtime.PrepareClientConfigRecovery` (prepare → mutate live → recover →
  assert live file == original **and** backup file == original).
  `activation/cover2_test.go:270-296` currently hand-builds the record and
  asserts the reversed direction, so **it passes with the bug present** — it is
  worse than no test.

> ### Status: fixed, with a mutation-checked regression
>
> `recovery.go` now reads the backup and writes the live file, matching
> `RestoreClientConfigRecovery` exactly, and the comment states the direction and
> cites ADR 0004 so the next reader cannot "fix" it back.
>
> Two tests, both in `internal/activation`:
>
> - `TestRecoverRestoresExactFileBackups` — **rewritten**. It used to create the
>   live file with `original`, leave the backup *nonexistent*, and then assert
>   the backup received `original`: the bug, asserted. It now seeds the backup
>   with `original`, the live file with `post-activation`, and asserts the live
>   file comes back as `original` **and the backup survives untouched**.
> - `TestRecoverUsesRealProducerRecords` — **new**. Drives `recover()` with
>   records from the real `deps.Runtime.PrepareClientConfigRecovery("claude", …)`
>   instead of hand-built strings, because a hand-built record is what let the
>   inversion live in the first place.
>
> **Mutation-checked.** With the bug deliberately re-introduced, both fail:
>
> ```
> --- FAIL: TestRecoverRestoresExactFileBackups
>     live file not restored from backup: "post-activation"
> --- FAIL: TestRecoverUsesRealProducerRecords
>     live client config not restored: "post-activation"
> ```
>
> With the fix restored, both pass, and `gofmt` and `go vet` are clean.
>
> Still open: the durable version of this fix is §5 item 1 — one shared
> `ParseRecoveryRecord`/`ApplyRecoveryRecord`, so `runtime` and `activation`
> cannot disagree again. Right now the two implementations merely happen to
> agree.

### P0-2 · `anypick add account` is published everywhere and does not exist

`src/go/cmd/anypick/main.go:238-252` (`cmdAdd`).

`cmdAdd` implements `anypick add <provider> --name <n> [--force --kind
--secret --profile]` and rejects anything but `fs.NArg() == 1`. There is **no
`account` subcommand**.

Published anyway:

- `src/go/testdata/parity/cli-tree.json:84-97` declares `add account` with flags
  `--current --new --auth-method --name --source --api-key --region
  --credential --secret --option` — none of which exist on `cmdAdd`.
- `docs/src/content/docs/getting-started.mdx:72,76,90,127`,
  `guides/accounts.mdx`, `guides/plugins.mdx:68-71`,
  `guides/terminal-ui.mdx:156`, `reference/cli.mdx:74-79`,
  `guides/gateways.mdx:13`.

- **Authority:** `CODE_REVIEW.md` §2 ("broken published contract"); §3.4 (docs
  are what users believe).
- **Failure mode:** `anypick add account codex --current --name personal` — the
  primary onboarding flow, and the first command every docs page shows — passes
  `["account","codex"]` as positionals, hits `fs.NArg()==2` with `--current`
  undefined, and exits `InvalidUsage`. Account capture cannot be started from
  the documented command.
- **Verified:** `main.go:238-252`, `main.go:216-221` (usage), and the fixture.

**Which shape is correct is a product decision, not a code fix.** Neither
command has the missing subcommand:

- `anypick account <list|remove|refresh|export|import>` — no `add`
  (`accounts.go:55-69`).
- `anypick add <provider> --name <n> [--force --kind --secret --profile]` — no
  `account`.

What actually captures a login is `anypick add <provider> --name <name>`, which
calls `a.Accounts.Save(ctx, provider, name, force)` (`main.go:265`) — it always
captures the *current* credential. There is **no `--current` / `--new`
distinction in the Go engine at all**, so the semantics `getting-started.mdx:90`
describes ("`--new` stashes the current credential") were either lost in the
TypeScript→Go port or never existed.

So decide one of: (a) the docs and fixture are fiction — rewrite them to the
real `add <provider> --name` form; or (b) `--current`/`--new` was real behaviour
that the port dropped — restore it. Either way **fix the gate first (2.3)**,
because the gate is what allowed both the command and the fixture to drift.

- **Regression:** a test that runs each fixture-declared path with the args its
  flags imply under `isolateEnv`, and asserts exit 0 plus the persisted effect.
  Plus: assert every flag the fixture declares is *accepted* by the real
  command.

### P0-3 · The live `~/.grok/auth.json` is rewritten in place, non-atomically, from a request handler

`src/go/internal/provider/grok_credential.go:321-339` (`writeGrokSession`),
reached from `grokUpstream` (`:342+`) → `grok.go` `GatewayUpstream()` →
`app/resolver.go:64-72` → **per request** from `hub/server.go:169` and
`pool_server.go:164`. `authPath` is the real `~/.grok/auth.json`, not a snapshot.

```go
return os.WriteFile(authPath, append(out, '\n'), 0o600)
```

- **Authority:** invariant #4 / ADR 0004. Every other credential writer in the
  repo does tmp + `chmod 0600` + `os.Rename` — `upstream/policy.go:264-288`,
  `transfer/backup.go:146`, `ownership/ownership.go:133`. This is the only one
  that truncates in place.
- **Failure mode:** x.ai refresh tokens rotate, so this fires while serving
  traffic, not during a user action. A SIGKILL or power loss between truncate
  and write leaves the user's real Grok login empty or half-written, with no
  recovery copy — `Backup` copies live→snapshot, never the reverse.
- **Verified:** read directly.
- **Regression:** seed `GROK_AUTH_PATH`, force a refresh, assert the target
  inode is *replaced* (rename), not truncated.

> ### Status: fixed, with a mutation-checked regression
>
> `writeGrokSession` now delegates to a new `writeGrokAuthAtomic`, which stages
> a temp file **beside the target** (so the rename cannot cross a filesystem),
> chmods it `0600`, and renames. The shape follows `upstream.WriteState` — the
> most careful of the three atomic writers already in the repo.
>
> Two tests, following the repo's `//go:build !windows` convention:
>
> - `grok_auth_atomic_test.go` (portable) — JSON stays valid, the *other*
>   sessions sharing the file survive, mode is `0600`, and no `.grok-auth-*`
>   staging file is left behind.
> - `grok_auth_atomic_unix_test.go` — asserts the **inode changes**. This is the
>   one that actually proves the rename: `os.WriteFile` truncates in place and
>   keeps the inode.
>
> **Mutation-checked.** With `writeGrokSession` reverted to `os.WriteFile`:
>
> ```
> --- FAIL: TestWriteGrokSessionChangesInode
>     auth.json inode unchanged — the file was truncated in place,
>     not replaced by rename
> ```
>
> The portable test still passes under that mutation, which is correct and worth
> saying out loud: it cannot see truncation. The inode check is the whole point.
>
> Still open: this is now the **third** atomic-writer implementation in the repo
> (`ownership.writeAtomic`, `upstream.WriteState`, `provider.writeGrokAuthAtomic`).
> §5 item 3 — the AST gate that fails the build when a credential file is
> written without a rename — is what stops a fourth appearing.

### P0-4 · The Gemini API key is written in cleartext to a snapshot `.env` on every request

`src/go/internal/app/resolver.go:53-57`:

```go
if wErr := os.WriteFile(filepath.Join(snapDir, ".env"),
    []byte("GEMINI_API_KEY="+pr.Secrets.APIKey+"\n"), 0o600); wErr == nil {
```

`hub/server.go:169` calls `ResolveUpstream` **per request**, so this rewrite
happens on every call.

- **Authority:** invariant #2 (secrets stay in their owning store — `profile`
  owns `profile.secrets`); invariant #4; invariant #6; and AGENTS.md's
  convention that "provider-specific behavior belongs on the provider" — this is
  a `switch (providerId)` in `internal/app`.
- **Failure mode:** (a) a concurrent `anypick backup` reads a torn `.env` and
  overwrite a good backup — the exact data loss invariant #4 exists to prevent;
  (b) a stale copy survives profile key rotation or deletion, so a deleted
  gateway key keeps resolving.
- **Verified:** read directly.
- **Regression:** rotate the profile key and assert the old key is absent from
  the snapshot dir.

> ### Status: fixed by removing the write entirely
>
> The resolver no longer writes anything. The key goes from the profile store to
> the `Upstream` in memory:
>
> - `internal/provider/gemini_credential.go` — the API-key branch of
>   `geminiUpstream` is extracted into `geminiKeyUpstream(key string)`.
>   `geminiUpstream` now calls it; behaviour is unchanged.
> - `internal/provider/gemini.go` — new `GatewayUpstreamWithKey(apiKey string)`,
>   mirroring `grok.GatewayUpstream()`. This is what puts the policy on the
>   provider instead of in a `switch (providerId)` in core.
> - `internal/app/resolver.go` — the `.env` write and the
>   `providerID == "gemini"` special case are both gone, replaced by an
>   interface assertion on `GatewayUpstreamWithKey`. Any provider that
>   implements it now works; `paths` is no longer imported.
>
> `TestSnapshotResolverResolvesGeminiGatewayProfile` gained the regression
> assertion: after resolving, **no `.env` may exist anywhere under the data
> root**. It matches on the filename rather than on the key's contents, because
> the profile store's own database legitimately holds the key — it is the
> owning store (invariant #6).
>
> **Mutation-checked.** Re-introducing the write:
>
> ```
> --- FAIL: TestSnapshotResolverResolvesGeminiGatewayProfile
>     resolver persisted the gateway credential to disk: [.../001/.env]
> ```
>
> A second defect surfaced while reading `geminiApiKeyFromSnapshot`: it checks
> `os.Getenv("GEMINI_API_KEY")` and `GOOGLE_API_KEY` **before** the snapshot
> `.env`. So the write was not merely redundant, it was the last resort of three
> — and it was the only one that persisted a secret to disk.
>
> Net effect: a per-request cleartext write is gone from the hot path, and
> rotation is now instantaneous with no stale copy to outlive it.

---

## 2. P1 — correctness risks and bypassed invariants

| # | Path | Issue |
| --- | --- | --- |
| 2.1 | `cmd/anypick/main.go:71` vs `:92-93` | **The TTY guard runs after the service graph is touched.** `app.Ready(..., SkipMaintenance: false)` runs plugin activation, `RecoverIncompleteOperations` (`app.go:331`) and `Leases.ReapStaleContext` (`app.go:339`) *before* `case "tui"` → `tui.Run` → the `isatty` check. Under cron or CI, `anypick tui < /dev/null` performs recovery — with P0-1 that **destroys backups** — and reaps the desktop app's live proxies, then returns `ErrNoTTY`. This is the AGENTS.md trap verbatim, and it amplifies P0-1. **Fixed — see below.** |

> ### Status: fixed for the explicit surface, with a mutation-checked regression
>
> The guard now runs in `run()` **before** `app.Ready`, gated on a new
> `isExplicitTuiSurface(args)` helper that deliberately does not consult config
> — it has to work before anything exists to read config from.
>
> `TestTuiWithoutTTYRefusesBeforeTouchingServices` (in `cmd/anypick/e2e_test.go`)
> redirects stdin to `/dev/null`, runs `anypick tui`, and asserts both that the
> exit code is `InvalidUsage` **and that the data root does not exist**.
> `isolateEnv` creates only `home`, never `data`, so a created root is direct
> evidence that startup ran.
>
> **Mutation-checked.** Disabling the guard:
>
> ```
> --- FAIL: TestTuiWithoutTTYRefusesBeforeTouchingServices
>     data root .../data exists — services were touched before the TTY guard
> ```
>
> **Residual, and why it is not fixed here.** A *bare* invocation
> (`anypick` with no args) resolves `ui.defaultSurface` from config, and config
> is only readable after `app.Ready` has opened the database — so that path
> still reaches the guard inside `tui.Run`, one startup later. Fixing it means
> either a standalone config read before bootstrap or moving the surface
> resolution earlier; both are composition changes, not a guard placement. The
> realistic cron/CI form (`anypick tui`) is now safe. |
| 2.2 | `internal/transport/adapter.go:180-194` | **Verified.** `GrokAccountAdapter` calls `baseAccountCapabilities("grok", nil)`; with `mutate == nil` the struct default applies, so `RequiresNativeAuthWrite=false` (`adapter.go:84`). But grok *does* write native auth: `provider/grok.go:152-165` `Restore` writes `p.authPath()` = `~/.grok/auth.json` via `os.WriteFile`. So `planner.go:499-512` never emits `StepWriteNativeAuth`. `anypick use claude --with grok/work` reports success while leaving the previous login live — and since e9d8a85 resolves the upstream from *that live session*, Claude is routed through the **wrong Grok account**, with `Current().IsLiveMatch` false so the mismatch is invisible in the quota readout. Dormant before e9d8a85, live now. |
| 2.3 | `cmd/anypick/help_parity_test.go:138-144` | `looksUnknown` matches only five `"unknown …"` strings, so `run(["add","account"])` printing `usage: anypick add <provider> …` **passes** the gate. Every sub-dispatcher that reports arity/flag errors via `usage:` is invisible, and declared `cmd.Flags` are never probed. This is why P0-2 exists. |
| 2.4 | `internal/proxy/upstream_serve.go:171-177`, `:200-204` | `if got, err := up.AuthProvider(ctx); err == nil { token = got }` — on error the token falls back to `up.Auth`, which `grokUpstream` never sets. An expired Grok refresh token yields `Authorization: Bearer ` on every request: upstream 401, with the real cause in no response, log, or `proxy status`. ADR 0006 §2 says fail closed. |
| 2.5 | `internal/provider/gemini_credential.go:180-184` (and `grok_credential.go:217-221`) | The raw OAuth refresh response body is embedded into the returned error, which `gemini_serve.go:222-225` → `writeGeminiError:454-461` writes **verbatim into the client's HTTP response body** and into the pool's `captureWriter`. Both use unbounded `io.ReadAll` where every other read in those files uses `io.LimitReader`. A non-2xx that echoes the rejected POST form puts `refresh_token=…` into the AI client's logs. Violates ADR 0006 §6 and §6's "nothing that prints a secret was added to a failure path". |
| 2.6 | `internal/activation/recovery.go:152-158` | If any backup is unreadable, `recover` returns *before* the `nativeAuthCheckpoint` block (`:163-187`), so the checkpointed real login is never restored. The developer stays switched to the new account permanently. |
| 2.7 | `docs/src/content/**`, `.github/ISSUE_TEMPLATE/**` | **Verified: 20 `pnpm` references survive in the docs site.** `partials/install.mdx:16-21` is the worst — it is the install page, and it tells the reader to run `pnpm install` / `pnpm dev --help` / `pnpm dev tui` under "Node.js 22.5+ and pnpm". Also `getting-started.mdx:28,57`, `guides/proxies.mdx:194`, `guides/tray.mdx:58-60,114-117`. `bug_report.yml:31-37` makes a **Node version mandatory** for every bug report. `feature_request.yml:22-25` and `docs/AGENTS.md:36-40` point at `src/sources/account-adapters.ts` (deleted) and state a capability policy that is wrong (opencode *does* cross providers). Verified absent: `package.json`, `pnpm-lock.yaml`, `scripts/`, `src/tray/`, `src/sources/`, `src/clients/`. This is the **third recurrence** of a family §8 says was fixed. |
| 2.8 | `internal/activation/recovery.go:155,169,181,190,196,204` | `deps.Journal.Update(...)` is called on the closure's `c` thread-through, but `journal.Update` discards it and manufactures `context.Background()` (`journal/journal.go:176`). Invariant #5. Today it is lost cancellation; the moment `internal/journal` acquires a coordinator lock — which invariant #5 requires — all six sites deadlock, and every one **discards the error** (`_, _ =`), so `RunRecovery` reports success while the entry stays `executing` forever. |
| 2.9 | `internal/hub/store.go:557` `RouteByToken` | Called on **every proxy request** (`hub/server.go:105`) with no `ctx`, and `RouteByTokenContext` performs `UPDATE proxy_hub_routes SET token_secret = …` (`:599-603`, `:626-630`) — an unlocked mutation whose name matches no verb in either AST guard. With `SetMaxOpenConns(1)` (`db.go:34`) an uncancellable query head-blocks the whole process. |
| 2.10 | `Makefile:246-247`, `:262-265` | `make app-verify` / `make artifact-check` default `ROOT`/`DERIVED_DATA_DIR` to empty; `filepath.Abs("")` returns the cwd (`src/go`), a real directory, so `InspectNodeFree` walks the **source tree** and prints `Node-free artifact OK`. A false green on the gate that enforces ADR 0020. |
| 2.11 | `AnyPick.xcodeproj/project.pbxproj:126,359-360,370` | `check-node-free-app.sh` is a `PBXFileReference` and an *input path*, but the only `shellScript` in the project is `bundle-go-helper.sh`. **Xcode never runs the Node-free check** — AGENTS.md's trap says it does. |
| 2.12 | `src/macos/AppDelegate.swift:117-139`, `:43-53`, `:189` | The smoke test asserts only `store.snapshotReady && serviceState == .ready`; the store→model→view hop is never observed, so reverting `bcf1284` leaves the window on Loading while CI reports `ready`. Meanwhile `AppDelegate` still contains four `DispatchQueue.main.async { … }` closures calling `@MainActor`-isolated methods — byte-for-byte the shape `bcf1284` removed. Lines 43-44 are the *only* presenters on the non-smoke launch path. |
| 2.13 | `.github/workflows/ci.yml:142` | `cargo check --locked` skips `#[cfg(test)]`. The Tauri handshake/capability state machine — `protocol.rs:437+`, `sidecar.rs:633+`, `main.rs:170+`, ~15 tests — is **never compiled or run in CI**. |
| 2.14 | `internal/db/tables.go:40-43` vs `internal/hub/store.go:344-346` | The written `SharedWith` reason says `"internal/hub": "reads only; config writes stay in internal/config"` — but `ensureHMACKey` executes `INSERT INTO config (key, value) VALUES (?, ?)`. **Verified.** `tables_test.go:113` accepts any non-empty reason and never inspects the SQL verb, so a write hidden behind a "reads only" label passes. |
| 2.15 | `internal/activation/executor.go:186-198`, `:271-275`, `:700-773` | **Verified.** Invariant #3: the executor branches on `plan.Transport.Capability`, not on steps — the switch sits *before* the step loop and its own comment says "the planner does not emit these". `StepValidateExternalDep` is a declared no-op in `runStep` (`:272-275` `return nil`) — a dead step. Failure mode: move that switch after the step loop and `MISSING_DEPENDENCY` fires *after* `WriteNativeAuth`, i.e. AnyPick has already overwritten the user's real login before reporting the proxy is not installed. |
| 2.16 | `src/go/anypick` | **A 16.9 MB compiled Mach-O binary is tracked by git.** `git ls-files src/go/anypick` returns it; `git check-ignore --no-index` does not, so it is neither ignored nor accidental. `src/go/anypick.exe` (18.3 MB) *is* ignored via `.gitignore:31 *.exe`, and `src/go/bin/anypick-dev` is untracked — so exactly one of three build outputs leaked in. It is dated 2026-08-24, i.e. **older than `e9d8a85`**, so it is a stale build sitting in the tree. Authority: ADR 0020 §8 (`make artifact-check` exists to assert the shipped artifact's composition — a checked-in binary bypasses it permanently) and `CODE_REVIEW.md` §4 ("no dead code"). Failure mode: permanent repo bloat — git history keeps it after deletion; and a stale binary that reads as current. Regression: extend `make paths` to fail on any ELF/Mach-O/PE file under `src/` that is tracked and not a committed fixture. |

---

## 3. P2 — worth fixing, or an owned follow-up

- `proxy/pool_server.go:152-170` — `s.resolved` caches the first `Upstream` for the process lifetime; fine for AuthProvider-backed engines, wrong for any static-`Auth` provider. `upstreamFor(_ *http.Request, …)` advertises per-request resolution it does not do.
- `provider/gemini_catalog.go:40-56,62-91` — the catalog cache is global and keyed by upstream base only, so two Gemini accounts on one base share a model list; no in-flight dedup.
- `provider/gemini_credential.go:239` — `json.Marshal(*c.ExpiryDate)` nil-derefs if `ExpiryDate` is nil. One refactor from a panic.
- `provider/grok_credential.go:109-140` — `pickGrokSession` iterates a map, so session choice is nondeterministic across restarts.
- `transfer/transfer.go:54-65` — `ExportEnvelope` stages in `os.TempDir()` then renames to `outPath`: EXDEV when the destination is another volume (`backup.go:135` stages beside the destination and is correct).
- `transfer/codec.go:234-247` — unknown `credentialKind`/`credentialClass` are silently dropped; the comment claims they are "rejected". An api-key/proxy-only account is downgraded to native and then restored over the real login.
- `cmd/anypick/plugins.go:169-173` — prints "runs as executable JavaScript in AnyPick's isolated plugin host … The host can receive credentials" for a host that always degrades to `ErrPluginHostRemoved`.
- `internal/hub/store.go:478-495` — `SetModelOwnerContext` attaches a route with an empty token, persisting `token_secret = HMAC(key, "")` into a `NOT NULL UNIQUE` column. Not an auth bypass, but only because `proxy.SafeEqual` refuses `""` in an unrelated package.
- `internal/presets/presets.go:37-52` — `List()` duplicates `ListContext` with a bare `s.DB.Query`.
- `internal/journal/journal.go:247-249` — the comment "No ctx-less query wrapper …" is false; `Get`, `Create`, `Update`, `ListIncomplete`, `ListRecent` all manufacture `context.Background()`.
- `src/macos/MenuBarChrome.swift` — `TrayCardSurface`, `TrayMetaPill`, `TrayRadioMark` (a hand-drawn radio control) and card fills inlined at `MenuBarHome.swift:60-65,234-237`, `MenuBarUseWith.swift:101-105,134-150`, `MenuBarFirstRun.swift:53-57`. Violates `src/macos/README.md`'s forbidden list. Three of the views are dead.
- `src/tauri/src-tauri/src/sidecar.rs:328` — unbounded, unfiltered `eprintln!` of child stderr, where `GoServiceProcess.swift:126` logs the same stream with `privacy: .private` and a 2 000-char cap. Invariant #2 names stderr.

---

## 4. §10 is stale — three of its open items are already closed

This pass corrected the review standard's own open-work list. Read this before
scheduling work against it.

| §10 says | Reality at `e9d8a85` |
| --- | --- |
| `coordinator_test.go` — "16 mutators still bypass `s.mutate`" | **2 exemptions**, not 16: `PrepareSnapshot` and `DiscardPreparedSnapshot`, both with written reasons. I enumerated every exported verb-matching method in `internal/store`: 10 route through `s.mutate`/`WithMutationLocks`, 2 exempted, **0 unguarded**. The guard is landable today. |
| `bindings/locking_test.go` — "six mutators take no caller `ctx`, eight `context.Background()` remain" | **Zero** `context.Background()` in non-test `internal/bindings` (only a comment at `bindings.go:108`). Both AST guards should pass. The *lock* gap is unchanged. |
| `cmd/anypick-release/version.go` "does not compile against `main`" | Compiles; `cargoToml` and `tauriConf` live at `cmd/anypick-release/main.go:35-36`. |

Also verified green: protocol version is **2** in all three places
(`buildinfo.go`, `protocol.rs`, `AppServiceClient.swift`); `VERSION`,
`MARKETING_VERSION`, `Cargo.toml` and `tauri.conf.json` are all **1.0.0**;
`default.profraw` at the repo root is **not** tracked (ignored by
`.gitignore:32`) — 238 KB of someone's stray `-coverprofile`.

**Action:** rewrite those three §10 bullets. An item that is open across two
passes is accepted risk, not open work (§11.3) — but an item that is *closed*
and still listed is worse: it hides that the guard is ready to land.

---

## 5. Gates these findings need (§5)

The P0s above are four bugs. Three of them are *families* — the same shape
already fixed once elsewhere — which is §5's signal that the wrong rung was
chosen.

1. **Rung 3 — one parser for the `src=>dest` contract (P0-1).**
   `runtime.RestoreClientConfigRecovery` and `activation.recover` interpret the
   same string and disagree. Extract `ParseRecoveryRecord`/`ApplyRecoveryRecord`
   into one package and add an AST guard forbidding any other
   `strings.SplitN(… "=>" …)` under `src/go`. Two implementations of one gate
   must reject the same shapes — §5's own rule — and the existing test already
   encodes the bug, so fixing the test alone will not hold.

   **Done.** New package `internal/compensation` owns the format:
   `Record{Src, Dest}`, `Parse`, `IsDelete`, `Apply`. Both
   `runtime.RestoreClientConfigRecovery` and `activation.recover` now call it,
   and the local `splitRecord`/`trimSpace` pair in `internal/runtime` is gone.
   `TestNoSecondRecordParser` walks the module and fails the build on any
   `strings.SplitN`/`Split`/`Index`/`Contains`/`HasPrefix`/`LastIndex` called
   with a literal containing `=>`, outside `internal/compensation`.

   Three deliberate details, each earned while writing it:
   - **Test files are skipped.** The first version flagged the literal anywhere
     and produced 20 hits, 18 of them noise (`"=> %q want %q"` in messages, a
     `setInterval(()=>{}, …)` fixture). A guard that cries wolf gets muted.
   - **The producer is exempt by construction.** It builds records with
     `src+"=>"+dest`, a concatenation rather than a call into `strings`, so
     matching on *calls* leaves it alone without an exemption entry.
   - **The exemption list is empty**, per §5's own rule that an exempted guard
     rots.

   **Mutation-checked.** Injecting `strings.SplitN(s, "=>", 2)` into
   `activation/recovery.go` fails the build:
   `record_test.go:211: … parsed in more than one place … recovery.go:238`.

2. **Rung 3 — make `help_parity_test.go` see what it claims to guard (P0-2).**
   Add `"usage: anypick"` to `unknownSentinels` and probe each fixture-declared
   flag (`flag provided but not defined` must fail). Without this, no amount of
   doc fixing compounds: the `add account` gap cannot fail a build.

3. **Rung 3 — credential-write atomicity (P0-3).** An AST test in
   `internal/provider` — same shape as `db/tables_test.go` — failing the build
   when a function `os.WriteFile`s a path derived from `auth.json` /
   `oauth_creds.json` / `.env` with no accompanying `os.Rename`. Zero-entry
   exemption map. `grok.Restore` and `writeGeminiOAuthCredentials` share the
   shape, and the next ported engine will copy it.

   **Done, and the shape I first proposed was the wrong one.** I wrote the
   scanner as a throwaway analysis before committing to a gate, and it found
   **12 sites** touching a credential file, all non-atomic — then the
   consolidation pass found **six separate hand-rolled atomic writers**, three
   more than the review had spotted:

   ```
   ownership.writeAtomic          fixed ".tmp" suffix -> two writers race
   upstream.WriteState            CreateTemp + Chmod + Rename
   provider.writeGrokAuthAtomic   CreateTemp + Chmod + Rename
   proxy.adoptReboundScopedChild  fixed ".tmp" suffix
   transfer.ExportEnvelope        staged in os.TempDir -> EXDEV across volumes
   transfer.ExportSelectedBackup  CreateTemp-beside-target (correct, duplicated)
   ```

   New package `internal/atomicfile` provides the one `Write`. All six now use
   it, and `TestNoHandRolledAtomicWrite` fails the build on a seventh.

   Two latent bugs fell out of the consolidation:
   - `ExportEnvelope` staged in the **system temp dir** and renamed to
     `outPath`, which fails with `EXDEV` whenever the destination is on another
     volume — the P2 in §3. Staging beside the target fixes it.
   - `ownership.writeAtomic` and `adoptReboundScopedChild` both used a **fixed
     `.tmp` suffix**, so two concurrent writers raced on one staging path.
     Randomised staging names fix it.

   One case was deliberately **not** folded in: `ExportSelectedBackup`'s
   non-overwrite branch uses `os.Link`, not rename, because a hard link cannot
   clobber an existing destination. That is a different operation with a
   different guarantee, so it keeps its own staging — and the gate does not fire
   on it, because the rule needs both a write and a rename.

   Two live-credential writers were fixed at the same time, both truncating the
   user's real login in place — the same class as P0-3:
   `client.claudeAdapter.Apply` (`~/.claude/settings.json`) and
   `provider.grok.Restore` (`~/.grok/auth.json`).

   One test had to change for the right reason:
   `TestAdoptReboundScopedChildNeverSignalsBeforePersist` injected its
   persistence failure by occupying `proxy.pid.tmp`, which depended on the
   fixed staging name. It now removes write permission on the runtime directory
   and restores it in `t.Cleanup`. **The test's contract is unchanged** —
   persist fails, the old pid stays authoritative, nothing is signalled.

   **Mutation-checked.** Injecting a `CreateTemp`+`Rename` pair into
   `upstream/policy.go` fails the build: `probeHandRolled hand-rolls an atomic
   write; use atomicfile.Write`.

   **Still open, and deliberately not gated:** the remaining non-atomic writes
   are to AnyPick-owned snapshot and backup files, which are recoverable by
   re-taking them. Requiring atomicity there too would be a much larger change;
   the gate as written protects against the failure that actually matters —
   another implementation drifting.

4. **Rung 4 — docs drift gate (2.7).** Extend `make paths` with a deny-list
   scan over `README.md`, `docs/src/content/**`, `.github/**`, `src/**/README.md`:
   `pnpm dev`, root `pnpm install`, `pnpm macos:design`, `src/tray/`,
   `src/sources/`, `src/clients/index.ts`, `scripts/parity-cli-tree.ts`,
   `go/go.mod`. Third recurrence.

   **The deny-list above is wrong, and measuring proved it.** I scanned before
   building, the way §5 item 3 should have been done the first time. Most of
   what that list would flag is legitimate:

   | Would-be hit | Why it is not drift |
   | --- | --- |
   | `adr/0018`, `adr/0019` — `pnpm dev`, `src/tray/` | ADRs are immutable records of the past. The project forbids editing an accepted ADR. |
   | `NATIVE_APP_CUTOVER.md`, `src/go/PARITY*.md` | Cutover and parity records, describing what was migrated *from*. |
   | `CODE_REVIEW.md:328,388` | Names the drift as a past finding — correctly. |
   | `AGENTS.md:76`, `docs/AGENTS.md:18` — `cd docs && pnpm install && pnpm dev` | **Correct and current.** The docs site *is* a separate pnpm project with its own lockfile. |
   | `README.md:61,91` — `src/go/go.mod` | Current path. I meant the pre-move `go/go.mod`; the pattern as written matches today's real path. |
   | `docs/dist/**`, `src/tauri/ui/node_modules/**` | Generated output and vendored code. |

   The actual rule is much narrower: **no document may present a `pnpm …`
   command as the way to run the anypick CLI, TUI or tray.** That is precise,
   and it hits exactly the real violations:

   ```
   docs/src/content/partials/install.mdx:16,20,21   <- the install page
   docs/src/content/docs/getting-started.mdx:28,57
   docs/src/content/docs/guides/proxies.mdx:194
   docs/src/content/docs/guides/tray.mdx:58-60,114-117,125
   docs/src/content/docs/reference/cli.mdx:7-8
   docs/src/content/docs/reference/troubleshooting.mdx:117,120
   src/macos/README.md:311                          <- pnpm macos:design:check
   .github/ISSUE_TEMPLATE/bug_report.yml:31-37      <- Node version, required
   ```

   A gate built on this rule must skip `adr/`, `docs/history/`, `docs/dist/`,
   `node_modules/`, and the historical cutover/parity records — and must
   distinguish `cd docs && pnpm …` (correct) from bare `pnpm dev …` (drift).

   **Not built, deliberately.** Landing it green means rewriting ~15 sites, and
   roughly half of them have no mechanical replacement: `pnpm dev tray …` and
   `pnpm macos:design*` refer to the **deleted tray app**, so the fix is a
   content decision — delete `guides/tray.mdx`, or rewrite it around the Tauri
   app — not a find-and-replace. `pnpm dev tui` → `anypick tui` is mechanical;
   the tray guide is not. A gate that lands red is the §10 failure mode again.

5. **Rung 3 — module-wide ctx guard (2.8, 2.9).** Generalise
   `bindings/locking_test.go`'s `TestNoBackgroundContextInProductionCode` to all
   of `internal/**`: fail on any `context.Background()` reached from a method
   that has a `…Context` sibling. Zero exemptions. This one guard subsumes five
   separate findings.

6. **Rung 4 — wire the Node-free gate up (2.10, 2.11).** Makefile guard on empty
   `ROOT`/`DERIVED_DATA_DIR`; an XCTest asserting a `PBXShellScriptBuildPhase`
   whose `shellScript` contains `check-node-free-app.sh`; and a **shared
   deliberately-bad fixture on disk** that both scanners are run against in CI
   — today Go's fixtures are an in-process table only, so the shell half is
   never mutation-checked at all.

7. **Rung 4 — `cargo test --locked` in CI (2.13).**

---

## 6. What is genuinely good

Stated because a review that only lists defects is not a review.

- The invariant architecture is real and it works. The table-ownership registry
  (`internal/db/tables.go`, 23 owners, 2 reasoned `SharedWith`), the coordinator
  AST guard (2 reasoned exemptions, 0 unguarded mutators) and the bindings ctx
  guards are enforced by the build, not by memory. Most codebases at this size
  have one of these; this one has three.
- Proxy secrets are held to a real boundary. Every listener binds loopback
  (`server.go:246`, `pool_server.go:502`, `service_plugin.go:118,696,995`,
  `hub/server.go:54`), `StartResult.Token` is kept out of every output, and hub
  route tokens are stored as HMAC-SHA256 rather than plaintext.
- Cross-language version facts agree in all seven places I checked — protocol
  version ×3 and release version ×4. `make protocol-sync` and
  `make release-check` are doing their job.
- The dead code was actually deleted. `src/tray/`, the TypeScript engine, the
  Node supervisor and the plugin host are gone from the tree, not shimmed. What
  remains is docs that still describe them (2.7) — a much cheaper problem than
  the code.

---

## 7. Step 1 is applied — the gate chain runs green

Step 1 has been done in the working tree. All three edits are provably
behaviour-preserving, and each was verified rather than assumed.

| Edit | Verification |
| --- | --- |
| `gofmt -w` on the seven files, using **go1.26.7** — the exact version CI runs | `gofmt -l .` clean under go1.26.7, so CI will agree |
| `types.go:20,23` — U+200B / U+200C literals → `"\u200b"` / `"\u200c"` | A temporary byte-level test asserted `ToolSignatureCarrier == 0xe2 0x80 0x8b` and `EmptyThoughtCarrier == 0xe2 0x80 0x8c` — **byte-identical to before**. Test removed afterwards. |
| `chat.go` — deleted `appendSystem` and `stringsHasSuffix` | Both had zero callers (`stringsHasSuffix` was called only by `appendSystem`); `go vet` clean afterwards |

Result — `make review` now runs to completion:

```
review gates green. 'make lint' (staticcheck) also runs in CI.
```

`make lint` separately: **0 findings** (was 4). `make test-race`: green, but run
on the pre-fix tree — the fixes cannot affect concurrency, and the full
post-fix `make test` in `make review` is green.

Working tree diff (9 files, 183 insertions / 198 deletions): the eight Go files
above plus `src/macos/MainWindow.swift`, which is **your** pre-existing
uncommitted change and was not touched. All of it is revertible with
`git checkout -- src/go/`.

> One gap surfaced while verifying: **no test in `internal/protocol/gemini`
> references either carrier constant.** A corrupted zero-width character would
> have shipped silently. The temporary test above is the regression it needs —
> worth keeping, since 2.16-style drift is exactly what it catches.

## 8. Status of the applied work

| Item | Status |
| --- | --- |
| Step 1 — unblock the gate (`fmt-check` + `lint`) | **done** (§7) |
| P0-1 — inverted compensation rollback | **fixed**, two mutation-checked regression tests |
| P0-2 — `anypick add account` | **open — needs your decision** (§1, P0-2) |
| P0-3 — grok non-atomic write | **fixed**, mutation-checked (inode assertion) |
| P0-4 — gemini `.env` per request | **fixed** — the write is gone, mutation-checked |
| 2.1 — TTY guard ordering | open |
| 2.16 — tracked 16.9 MB binary | open |
| §5 gates | open — none built yet |

Working-tree diff (17 modified, +453 / −246):

```
src/go/cmd/anypick/main.go                      |  23 +    2.1 TTY guard
src/go/cmd/anypick/e2e_test.go                  |  38 +    2.1 test
src/go/internal/activation/recovery.go          |  21 +-   P0-1
src/go/internal/activation/cover2_test.go       |  76 +--   P0-1 tests
src/go/internal/app/resolver.go                 |  34 +-   P0-4
src/go/internal/app/resolver_test.go            |  29 +-   P0-4 test
src/go/internal/provider/gemini.go              |  17 +    P0-4
src/go/internal/provider/gemini_credential.go    |  42 +-   P0-4
src/go/internal/provider/grok_credential.go     |  46 +-   P0-3
src/go/internal/protocol/gemini/chat.go         |  14 -    lint: dead code
src/go/internal/protocol/gemini/{models,responses,thinking,types}.go
                                                           lint: gofmt + escapes
src/go/internal/provider/grok_upstream_test.go  |  18 +-   gofmt
src/go/internal/proxy/auth_provider_test.go     |   8 +-   gofmt
src/macos/MainWindow.swift                      |  15 +-   <- yours, untouched
```

Three untracked files, none committed:

```
CODE_REVIEW_2026-09-03.md                                <- this report
src/go/internal/provider/grok_auth_atomic_test.go
src/go/internal/provider/grok_auth_atomic_unix_test.go
```

Revert the modifications with `git checkout -- src/go/`; delete the two new test
files to drop P0-3's regression entirely.

### Suggested remaining order

1. **P0-2 — the only P0 left, and it is a product decision, not a code fix.**
   Decide (a) the docs are fiction, or (b) `--current`/`--new` was real
   behaviour dropped in the TypeScript→Go port. Then fix the parity gate (2.3)
   regardless, or this recurs.
2. ~~§5 item 1 — the shared recovery parser~~ — **done** (see below).
3. ~~§5 item 3 — the credential-write atomicity gate~~ — **done** (see below).
4. §5 item 4 — docs drift gate. The rule is now precise (see §5 item 4) but
   landing it green needs a **content decision about `guides/tray.mdx`**: the
   tray app it documents was deleted, so the choice is delete the guide or
   rewrite it around the Tauri app. Everything else is mechanical.
5. 2.1's residual — bare invocation resolving `ui.defaultSurface: "tui"` still
   reaches the guard one startup late. Needs a composition change, not a guard
   move (see §2.1).
6. 2.16 — cheap, but `git rm` only removes it from the tip; the object stays in
   history. Decide whether that matters first.
7. Then rewrite the three stale §10 bullets in §4.

### What is genuinely left, in one line each

Only one item below is blocked on code. The rest are decisions.

- **P0-2** — docs fiction or dropped behaviour? (product)
- **§5 item 4** — delete or rewrite `guides/tray.mdx`? (content)
- **2.16** — is a 16.9 MB object in git history acceptable? (judgement)
- **2.1 residual** — move surface resolution before bootstrap? (architecture)

**Still not run — gate not run:** `make app-test`, `make tauri-check`,
`cd docs && pnpm build`, and CI history (`gh` is not installed). None is blocked;
they simply were not run in this pass.

`make test-race` **was** re-run after the P0-1 fix — recovery executes inside
`lock.WithMutationLocks`, which §6 puts in race-detector territory. Result:
**40 packages green, no data race.** Same caveat as §0.3: this does not
adjudicate 2.8 or 2.9, which `-race` cannot see.
