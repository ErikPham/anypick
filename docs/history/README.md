# Historical design documents

These are **not** current documentation. They are the design and planning
documents written while `anypick` was being built, kept for provenance: they
explain *why* several decisions were made, and a number of source comments still
cite their section numbers.

Do not treat anything here as a description of how the code works today. The
authoritative sources are, in order:

1. The code itself.
2. [`../../adr/`](../../adr/) — accepted architecture decisions, each with
   context and consequences. Start here for "why is it like this?".
3. [`../../README.md`](../../README.md) — user-facing behaviour and commands.
4. [`../../CONTRIBUTING.md`](../../CONTRIBUTING.md) — layering, how to add a
   provider or client, lint policy.

## Contents

| Document | What it is | Status |
| --- | --- | --- |
| `anypick-cli-dx-redesign-spec-final.md` | The original CLI/DX specification. Source of the `§`-numbered references in code comments (e.g. exit codes in `src/utils/errors.ts`). | Largely implemented; superseded in places by ADRs. Despite the `-final` suffix, later plans revised it. |
| `DESIGN.md` | Long-form architecture narrative: layering, stores, registries, adapter contracts. | Mostly accurate in spirit; specifics have drifted. |
| `DESIGN-TUI.md` | TUI screen-by-screen design. Source of the `DESIGN-TUI §n` references in `src/tui/**`. | Mostly implemented. |
| `FRAMEWORK-IMPROVEMENT-PLAN.md` | Proposed framework hardening work. | Partly done, partly obsolete. A proposal, never a description. |
| `OPENCODE-GATEWAY-IMPLEMENTATION-PLAN.md` | Plan for the OpenCode gateway/proxy. | Implemented. |
| `anypick-feature-map.md` | Early feature inventory. | Historical. |
| `ARCHITECTURE_AND_ROADMAP_2026-09-08.md` | Joint architecture assessment and roadmap snapshot from the native cutover. | Historical; the roadmap items were absorbed into the cutover tracker and ADRs. |
| `AUDIT_2026-09-03.md` | Pre-release audit snapshot. | Historical; its findings were folded into `CODE_REVIEW.md` and the release-readiness records. |
| `CODE_REVIEW_2026-09-03.md` | Dated review of the tree as of 2026-09-03. | Historical; the living standard is [`../../CODE_REVIEW.md`](../../CODE_REVIEW.md). |
| `BACKEND_ARCHITECTURE_2026-09-07.md` | Backend architecture snapshot from the native cutover window. | Historical; superseded by ADRs and the consolidated architecture note. |
| `SYSTEM_ARCHITECTURE_2026-09-07.md` | System architecture snapshot. | Historical; consolidated into `ARCHITECTURE_AND_ROADMAP_2026-09-08.md`. |
| `TEAM_QUALITY_BASELINE_2026-09-08.md` | Quality baseline / G0–G1 checklist. | Historical; living gates live in `CODE_REVIEW.md` and `make check`. |
| `CODE_REVIEW_AUDIT.md` | Audit notes feeding the review standard. | Historical. |
| `CODE_REVIEW_ENFORCEMENT_2026-09-09.md` | Enforcement plan for review gates. | Historical; hooks live in `.githooks/`. |
| `CODE_REVIEW_HOOKS_2026-09-11.md` | Commit-msg / pre-push hook design notes. | Historical; see `.githooks/`. |
| `NATIVE_APP_CUTOVER.md` | Native cutover tracker matrix. | Historical; cutover complete (ADR 0020). |
| `RELEASE_READINESS_2026-09-09.md` | Dated release-readiness checklist. | Historical. |
| `UI_AUDIT_PLAN_2026-09-24.md` | macOS UI/UX audit and fix plan (2026-09-24). | Historical; source fixes landed; fixture verification remains. |
| `TAURI_PARITY_TRAIL.md` | Tauri↔macOS parity decision trail. | Historical; port marked complete against `make check-parity`. |
| `PROPOSED_POOL_PIPELINE_UNIFICATION.md` | Spike plan for folding the pool capture leaves into the committing egress leaves via a sink parameter. | Not started; the duplication today is structural, not a live behavioral bug. |

## If you are adding a code comment

Cite an **ADR number**, not a `§` section from these files. ADRs are short,
immutable once accepted, and state their own consequences; a section number in a
76 KB superseded spec sends the next reader on an archaeology expedition. Where
no ADR covers the decision, write the reason inline instead.
