# Branch ruleset (required checks)

In-repo, verifiable record of the GitHub branch ruleset that protects `main`.
Repository files cannot enforce a hosting setting, so this document is the
specification Erik enables in **Settings → Rules → Rulesets** (or via the
`gh` API). The code side (`CODE_REVIEW.md §10`) is done: the `Review metadata`
workflow and the `make ci` jobs exist and run on `pull_request_target` /
`pull_request`.

## Scope

- Target branches: `main`, and any `release/*` branch.
- Enforcement status: **active** (not "evaluate only") once enabled.

## Required status checks

All of the following must pass before a merge is allowed. Names are the exact
check names GitHub reports (the job `name:` fields), grouped by workflow.

**Workflow `Review metadata`:**

- `Review metadata`

**Workflow `CI`:**

- `Format / Vet / Version sync`
- `Go / Vet & Test`
- `Go / Windows x64`
- `Tauri app / Linux check & UI build`
- `macOS app / Xcode & universal helper`
- `Docs / Typecheck / Lint / Build`

Strict requirement: the check names above must match the jobs in
`.github/workflows/ci.yml` and `.github/workflows/review-metadata.yml`. If a job
is renamed, update this file in the same change — a renamed job silently drops
out of the required set and the ruleset stops protecting that gate.

## Other rules

- Require a pull request before merging (no direct pushes to `main`).
- Require conversation resolution before merging.
- Dismiss stale approvals when new commits are pushed.
- Require review from Code Owners (`.github/CODEOWNERS` exists and covers the
  credential boundary).
- Do not allow bypass (no exemption for administrators); if a break-glass path is
  ever needed, add a named, audited bypass team rather than a blanket bypass.

## Why

`CODE_REVIEW.md §10` lists this as the only remaining open item: the review
classification and the build/test evidence must be **required** (not merely
run) before merge, so a change cannot land on `main` without both the human
review record and a green `make ci`. Without this ruleset, those jobs run but a
reviewer can merge around them.
