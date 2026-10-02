# AnyPick — Architecture Blueprint & Long-Term Roadmap

**Software Architect deliverable · 2026-09-08**
Consolidates `SYSTEM_ARCHITECTURE_2026-09-07.md`, `BACKEND_ARCHITECTURE_2026-09-07.md`,
and `TEAM_QUALITY_BASELINE_2026-09-08.md`. Where they have moved since, this document
wins. It is the single entry point for "what does the system look like and where is it
going."

---

## 0. TL;DR for the busy

1. **The foundation is right — do not rewrite it.** One binary for every surface
   (ADR-0020), tooth-gated invariants (21 ADRs, every gate already mutation-checked),
   and persistence done *correctly* precisely where it is not allowed to be wrong
   (real users' credentials).
2. **"Scale" here is not RPS.** It is four axes: number of supported clients/providers,
   number of distribution platforms, how many days a process survives without leaking
   or crashing, and how many contributors can land changes without breaking an
   invariant. Design and the roadmap below are measured against those four axes.
3. **The biggest growth bottleneck is distribution, not code.** Unsigned, release is
   dispatch-only and macOS-only by default, the npm adapter does not exist, and
   `anypick update` used to promise a package that wasn't real (fixed 2026-09-07 to
   query GitHub Releases instead).
4. **G0 and G1 are largely DONE.** Per `TEAM_QUALITY_BASELINE_2026-09-08`: the serving
   crash is fixed, `internal/egress` exists, observer + semaphore + lease TTL + ring
   deadline all landed, and the facade leak dropped **99 → 33** via an AST gate. Three
   of the seven "pending" decisions (§6) are in fact already closed.
5. **G2 is blocked by two decisions only Erik can make** (Apple signing identity and
   publication approval). Everything else in G2 is buildable.

---

## 1. What kind of system AnyPick is — and what "scale" means

AnyPick is **not a cloud service**. It is local infrastructure: a credential broker +
loopback proxy + desktop app that runs on one developer's machine, stays alive for many
days, and holds write access to *other tools' credential files*. A failure here does not
mean "lost request" — it means **corrupted real developer logins**. Every design choice
below is downstream of that.

The four real scale axes, and the design consequence of each:

| Scale axis | Measured by | Design consequence |
| --- | --- | --- |
| **Client / provider breadth** | Effort to add one new client | Must be **O(1)**: one adapter row + one registry entry; `adapter_test.go` and the TUI reachability test *enforce* this. |
| **Process longevity** | 30 days alive, no leak/stall/crash | Caches must be bounded, locks correct, no orphan goroutines. The serving layer decides this. |
| **Distribution surface** | Installable on macOS/Win/Linux in one command | One logical artifact, many channels; signing/notarization is *part of the architecture*, not end-of-project polish. |
| **Human contributors** | Can contributor #2 land a change without breaking an invariant? | Invariants must live in runnable gates, not in one person's head. |

---

## 2. Architecture blueprint

### 2.1 Context

- **Actors:** a developer; their AI coding clients (Claude Code, Codex, opencode, zcode,
  dsh, …); upstream vendors (Anthropic, OpenAI, Gemini, …).
- **AnyPick's job:** point those clients at accounts the developer already owns, by
  rewriting the clients' credential/config files and standing up local loopback proxies
  authenticated with a per-instance secret (ADR-0006), bound to loopback only.

### 2.2 Containers (the deployable pieces)

| Container | Role | Notes |
| --- | --- | --- |
| **One Go binary** (`anypick`) | CLI, TUI, desktop service, proxy, hub | ADR-0020: a single binary; the artifact is **Node-free** (`artifact-check`). |
| **macOS app** (SwiftUI) | Owns lifecycle of the core; native UI | One Swift tree (ADR-0018). Talks to core over a versioned, bounded stdio protocol. |
| **Tauri app** (Rust + React) | Windows / Linux front end | Same protocol as macOS — presentation, not a second implementation. |
| **Docs site** | `docs/` (separate pnpm project, own lockfile) | Not part of the runtime artifact. |
| **Release machinery** | GitHub Actions (dispatch-only today) | Produces signed/notarized/SBOM artifacts + GitHub Release assets. |

### 2.3 Components & layers (in-repo)

```
┌─ Surfaces ───────────────────────────────────────────────────────┐
│  CLI (cmd/anypick)   TUI (internal/tui)   desktop-service         │
│  ── stdio protocol v2: bounded frames, version handshake ──        │
│  AnyPick.app (Swift, owns lifecycle)    Tauri (Win/Linux)         │
└──────────────────────────────┬───────────────────────────────────┘
                               ▼
┌─ Composition root: internal/app (App.compose) ────────────────────┐
│  services: accounts · proxy · config · plugins · runtime · hub    │
│  activation (plan → journal → execute) · desktop.Server           │
└──────────────────────────────┬───────────────────────────────────┘
                               ▼
┌─ Domain ─────────────────────────────────────────────────────────┐
│  transport (capability pairing) · client/provider registries     │
│  hub (model-routed) · pools (failover ring) · upstream/egress    │
└──────────────────────────────┬───────────────────────────────────┘
                               ▼
┌─ Persistence (one table, one owner; internal/db/tables.go) ───────┐
│  store · journal · pools · hub · bindings · presets · profile    │
│  · plugins · modelcache — every mutation via coordinator (0009)   │
└──────────────────────────────┬───────────────────────────────────┘
                               ▼
        Other tools' credential files (atomic write, ADR-0004)
        Vendor upstreams (via internal/egress, one transport)
```

**Layering rule (enforced by AST gate, not by convention):** CLI, TUI and the desktop
protocol call *services*, never stores directly. The facade leak is now held at **33
call sites** by a structural AST gate (`facade_test.go`); 7 patterns are hard-forbidden,
3 are irreducible budgets (`a.Store` 3, `Deps.Store` 8, `a.Hub` 2 — these are transfer/
inspection/modelcache peers and `activation.Deps`, not laziness).

### 2.4 The request pipeline: one core, four front doors

This is the single most important structural rule in the serving layer
(`BACKEND_ARCHITECTURE` §3). Today four pipelines exist in parallel
(`proxy.Server`, `PoolServer`, `hub.Server`, `ServeAuthorized`). The crash they caused is
fixed, but the root cause — "every new timeout/metric/limit must be added in four places"
— is only half-closed. The target shape:

```
  ┌─ Front doors (thin, one per deployment shape) ───────────────┐
  │  · authN:  route token (hub) | instance token (proxy/pool)    │
  │  · source: by model (hub) | fixed (proxy, pool)               │
  │  · body:   passthrough | buffered | rewriting                 │
  └───────────────────────────┬──────────────────────────────────┘
                              │  Plan{ctx, body, candidates, rewrite}
                              ▼
  ┌─ Pipeline core (exactly one implementation) ─────────────────┐
  │   1. resolve    → Upstream for candidate                     │
  │   2. egress     → shared transport + deadline policy         │
  │   3. classify   → ok | quota-class | transient               │
  │   4. failover   → ordered ring + cooldown single-flight      │
  │   5. translate  → adapter protocol (provider-agnostic)       │
  │   6. observe    → in-flight, latency, status, failover       │
  └───────────────────────────────────────────────────────────────┘
```

**Done:** crash fix, `internal/egress` (one tuned `*http.Transport`), hub lifecycle
race fix, observer + semaphore (256-slot, no queue, 503 + `Retry-After` over cap), ring
deadline (2m pre-commit, never cuts a started stream), lease heartbeat/TTL.
**Not done:** unifying all four doors onto one core implementation + a single unified
body policy (`TestPipelineBodyPolicy`). That is the remaining SEV1-in-nature debt even
though its symptom is gone.

### 2.5 Persistence model

- **One table, one owning package.** `internal/store`, `internal/journal`,
  `internal/pools`, `internal/hub`, `internal/bindings`, `internal/presets`,
  `internal/profile`, `internal/plugins`, `internal/modelcache` each own their slice of
  one SQLite DB (`~/.anypick/anypick.db`). Services never reach past a store into SQL.
- **Every persisted mutation runs under the coordinator** (ADR-0009/0011) with sorted,
  scoped, re-entrant locks — one scope *per provider*, because all accounts are reached
  through that provider's single live credential file.
- **`writeMeta` is the atomic commit point** for an account snapshot (ADR-0004): a failed
  backup/import can never destroy the previously saved account.
- **Mutating store methods take `ctx` as first arg and have no ctx-less form** — the
  compiler now refuses the mistake.

### 2.6 Tech-stack rationale (why these choices)

- **Go single binary** — one artifact, no Node runtime in the shipped package, easy to
  reason about a long-lived process. ADR-0020.
- **Swift (macOS) + Tauri/Rust (Win/Linux)** — native controls the user already trusts;
  both are *thin* presentations over the same stdio protocol, so logic lives in one place.
- **SQLite + coordinator** — local-only, file-backed, with a single locking authority so
  correctness does not depend on callers remembering to lock.
- **AST gates over comments** — invariants that live only in prose drift (proven by
  `CODE_REVIEW.md` §10 being wrong 3/4 of the time). Gates that parse the AST cannot
  drift silently.

---

## 3. Key invariants (signed) and how they are enforced

These are load-bearing. Touching any of them requires a new ADR, never an edit to an
accepted one.

| ADR | Invariant | Enforcement |
| --- | --- | --- |
| 0002 | Trust boundary on import/export | review + tests |
| 0003 | Process & lease ownership | instance-id echo + verify argv; PID is a *veto*, not a sentence |
| 0004 | `writeMeta` atomic commit | tests; backup never destroys live account |
| 0006 | Proxy secret never leaves the execution boundary | `observe.Begin(route string)` takes **no** `*http.Request`; AST gate `TestNoRequestTypeReachesThisPackage`; `TestObserverNeverRecordsToken` |
| 0007 | Bindings are project-scope metadata only | review |
| 0008 | Crash backup is owner-only | review |
| 0009 / 0011 | Coordinator, provider-scope lock | `coordinator_test.go` fails the build on bypass; ctx-first required |
| 0013 | One hub, exact model ID, no first-source-wins | `TestHubDoesNotMixTwoPinsForOneModel`, `TestHubDoesNotRewriteWhenNoMapping` |
| 0016 | Capability routing lives on the adapter | `adapter_test.go` requires an explicit expectation per registered client |
| 0018 | One Swift tree | `sources.txt` assert |
| 0020 | Node-free artifact | `artifact-check` |
| 0021 | Native build installs but never executes plugins | `artifact-check` + tests |
| 0022 | `bindings.Store` self-locks via coordinator | `bindings.Store.mutate` → `lock.WithMutationLock` |

**Process invariant:** every new invariant ships with a mutation-checked gate. A gate that
has never failed has never been proven — so every gate is mutation-checked (break the code
→ FAIL; restore → PASS). This is the team's highest-leverage habit and must not regress.

---

## 4. Scalability strategy per axis

| Axis | Today | Target | Lever |
| --- | --- | --- | --- |
| **Client/provider** | O(1) by rule, enforced by `adapter_test.go` | Keep it O(1) as we add clients | Adapter row + registry entry + TUI allowlist; never a `switch(providerId)` in core |
| **Process longevity** | Crash fixed; observer + semaphore in | 30-day soak with zero leaks | Unify pipeline core (§2.4); keep caches bounded; no orphan goroutines |
| **Distribution** | GitHub Release (macOS, dispatch-only), unsigned | Signed + notarized + SBOM; Win/Linux published; optional npm thin adapter | G2/G3; blocked by signing creds + publication approval |
| **Contributors** | Bus factor = 1; gates hold invariants | Contributor #2 lands safely | CODEOWNERS + branch ruleset *before* push access; service boundaries + AST gates |

---

## 5. Long-term roadmap (status as of 2026-09-08)

Each phase lands independently; no phase requires a later one to exist.

### G0 — Serving stability + remove the broken promise ✅ *mostly done*
| Work | Done? | Gate |
| --- | --- | --- |
| Fix `QuotaGuard` crash + `internal/egress` | ✅ | `TestGuardConcurrentUseIsRaceFree`, `TestTransportIsTuned` |
| Observer + semaphore + listener limits | ✅ | `TestObserverNeverRecordsToken`, `TestFailoverStampedeCollapses` |
| `anypick update` → GitHub Releases, drop npm lie | ✅ | manual review |
| Lease heartbeat/TTL | ✅ (decision #4 closed) | `ReapStale` tests |
| Total ring deadline | ✅ (decision #5 closed) | deadline tests |
| **Unify four doors onto one core + unified body policy** | ⬜ | `TestPipelineBodyPolicy` — *the one remaining SEV1-in-nature item* |

### G1 — Close the Go structural debt ✅ *mostly done*
| Work | Done? | Gate |
| --- | --- | --- |
| accounts service readers; route `hub_snapshot` reads | ✅ | `facadeBudget` 32 → 30 locked |
| bindings lock via coordinator (ADR-0022) | ✅ (decision #6 closed) | `coordinator_test` extended |
| six ctx-less read wrappers → ctx-first | ✅ | existing ctx-first gate |
| facade leak 99 → 33 via AST gate | ✅ | `facade_test.go` structural gate |
| long-function ratchet (65 → 51); `buildSnapshot` 532→78, `handleMutate` 275→68 | ✅ | `TestLongFunctionCountDoesNotGrow` |
| invisible-debt gates (TODO markers, §10 drift) | ✅ | 2 new gates |
| **Route the 3 irreducible budgets to 0** (transfer/inspection/modelcache as services) | ⬜ | remove `facadeBudget` entirely |

### G2 — Native cutover + real release ⛔ *blocked by decisions #1, #2*
| Work | Blocker | Gate |
| --- | --- | --- |
| Native M1–M4 (live-parity, contract harness, archive/install, perf budgets) | none | `NATIVE_APP_CUTOVER.md` matrix |
| M5: Tauri installed smoke on Win/Linux | none | CI tauri job |
| M7: delete `src/tui` after Go TUI v1 suffices | none | `make paths` |
| **M8: signing + notarize + SBOM** | **Apple signing identity (Erik)** | `spctl` passes |
| **M9: final cutover + audit** | **Publication approval (Erik)** | full CI |

### G3 — Expansion (4–18 months)
| Work | Gate |
| --- | --- |
| npm thin adapter published (ADR-0020 §4) | `npm install -g anypick` smoke on 3 OS |
| Win/Linux published (drop `macos-only` default) | release.yml publishes all three |
| Add clients/providers O(1) | `adapter_test` + TUI reachability |
| Second observability consumer (desktop shows numbers) | app-test |
| Plugin ecosystem within ADR-0021 limits | `artifact-check` + tests |

---

## 6. Decision register (the seven from §6 of the prior doc)

| # | Decision | Owner | Status | Recommendation |
| --- | --- | --- | --- | --- |
| 1 | Apple signing identity + notarization creds | **Erik** | ⛔ open — blocks all of G2 | Resolve first; the only blocker not fixable in code. |
| 2 | Publication approval (release is dispatch-only) | **Erik** | ⛔ open — blocks G2/G3 | Write the "confident to publish" bar in text, attach to M9. |
| 3 | npm package name/scope | **Erik** | ⛔ open | **Register/hold the name now** (empty placeholder) — squatting risk is cheap to avoid. |
| 4 | Lease TTL/heartbeat | Architecture | ✅ **closed** (2026-09-07 §8.5) | — |
| 5 | Total ring deadline | Architecture | ✅ **closed** (2026-09-07 §8.6) | — |
| 6 | Bindings lock via coordinator | Architecture | ✅ **closed** (ADR-0022) | — |
| 7 | `facadeBudget` reduction schedule to 0 | Architecture | 🟡 in progress (99 → 33) | Commit to closing the 3 irreducible budgets next; then delete the ratchet. |

**Note:** the prior architecture doc still lists #4–#6 as "pending." They are done. The
drift itself is the lesson behind F4 — decisions recorded in prose rot; gates and this
register are the source of truth.

---

## 7. Risk register

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| **Bus factor = 1** | high | severe | CODEOWNERS + branch ruleset (require review job + build/test) *before* granting push; document hosting config in-repo (`docs/SECURITY`). |
| **Distribution is the growth ceiling** | high | high | Unblock decisions #1–#3; never open a new channel before the current one is sealed (cradle→run→update). |
| **Gate trust without verification** | medium | high | Every gate mutation-checked; never `|| true` on a check command; assert on *result*, not just exit code (F1). |
| **Parallel sessions editing same tree** (`src/macos`) | medium | medium | `stat` before editing Swift/pbxproj; CI strong enough to catch conflicts. |
| **Facade leak creeps back** | low (now gated) | medium | AST gate forbids 7 patterns; finish the 3 budgets to remove the ratchet entirely. |
| **Four-pipeline divergence returns** | medium | high | Land the unified core (G0 remaining item) so timeouts/metrics/limits have one home. |

---

## 8. Contributor growth plan (1 → N without breaking invariants)

The current gates make *code* safe; they do not yet make *people* safe. Sequence:

1. **Hosting guardrails first.** Enable the branch ruleset (require the Review-metadata job
   + build/test) and write down where it is configured (it cannot be enforced by a file in
   the repo). Add `CODEOWNERS` now that a second person exists.
2. **Push access only after the above.** Do not loosen gates for new contributors —
   tighten the process around them.
3. **Service boundaries as the on-ramp.** The AST facade gate already blocks the dangerous
   path. New contributors should add behaviour through services; the type system now
   refuses the shortcut.
4. **Review norm:** a reviewer must answer *"if this is wrong, what catches it?"* If the
   answer is "nothing," it needs a test or a gate — not a comment.
5. **Process fix for F5:** every 4 feature commits, 1 refactor/test commit. The 33% `fix` /
   7% `refactor` ratio is paying interest on structure repeatedly.

---

## 9. Guardrails — what not to do

- **Do not cloud-ify, microservice-ify, or add a queue/broker/Redis.** This is a one-user
  loopback proxy; every new component is a new failure point, not more capacity.
- **Do not rewrite.** Every large change is "extract gradually, each step lands
  independently" (the six-step serving plan proves this works).
- **Do not break a signed invariant.** Read the ADR list before touching. New invariant →
  new ADR + mutation-checked gate.
- **Do not open a new distribution channel before the current one is sealed** end-to-end
  (install → run → update). The `anypick update` npm lie is the cautionary tale.
- **Do not loosen gates for new contributors.** Opposite: add CODEOWNERS + branch ruleset
  *first*, then grant push.

---

## 10. Recommended next 30 / 90 days

**Next 30 days (engineering, unblocked):**
1. Finish the G0 remaining item: unify the four doors onto one pipeline core + unified body
   policy (`TestPipelineBodyPolicy`). This retires the last SEV1-in-nature debt.
2. Close the 3 irreducible facade budgets (transfer/inspection/modelcache as services) →
   delete `facadeBudget` entirely.
3. Land Native M1–M4 (no external blocker).
4. Register/hold the npm name (decision #3) — 10-minute task, removes a future risk.

**Next 90 days (needs Erik's two decisions):**
5. Once #1 (signing) lands: M8 signing + notarize + SBOM.
6. Once #2 (publication) lands: M9 final cutover + audit; flip release off dispatch-only.
7. M5 Tauri smoke on Win/Linux; M7 delete `src/tui`.
8. Enable branch ruleset + CODEOWNERS; onboard contributor #2 under the plan in §8.

**The one thing that gates the whole second half is a calendar decision, not an engineering
one:** get Apple signing credentials and a written publication bar. Everything else is
buildable today.
