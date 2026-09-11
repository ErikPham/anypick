#!/bin/sh
#
# Mutation suite for the commit-msg gate: .githooks/test.sh
#
# CODE_REVIEW.md §5: a gate nobody has seen fail is a gate nobody has proven.
# Run this after any edit to commit-msg. Every case must print PASS.
#
# Deliberate shape of the suite: two of the cases are real subjects from this
# repository's history that the gate should have rejected, so the suite is also
# a regression record for the inconsistency it was written to fix.

set -u

here=$(cd "$(dirname "$0")" && pwd)
hook="$here/commit-msg"
msg_file=$(mktemp -t anypick-commit-msg)
trap 'rm -f "$msg_file"' EXIT

# Pin the author identity. The hook reads `git var GIT_AUTHOR_IDENT`, and git
# honours these two variables, so the self-review cases below are deterministic
# and cannot pass vacuously on a machine with no identity configured.
export GIT_AUTHOR_NAME="anypick-suite-author"
export GIT_AUTHOR_EMAIL="suite@example.invalid"
author_name="$GIT_AUTHOR_NAME"

pass=0
fail=0

run() {
  name=$1
  want=$2
  msg=$3
  printf '%s\n' "$msg" > "$msg_file"
  out=$("$hook" "$msg_file" 2>&1)
  got=$?
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
    printf 'PASS %s\n' "$name"
  else
    fail=$((fail + 1))
    printf 'FAIL %s -- exit %s, wanted %s\n' "$name" "$got" "$want"
    printf '%s\n' "$out" | sed 's/^/       /'
  fi
}

# --- real subjects from this history that must be rejected --------------------
run "F2: prefix (commit 34a6980)" 1 "F2: route proxy cluster behind proxy.Service + replace substring ratchet with AST facade gate"
run "engine: prefix (commit c38c8e7)" 1 "engine: measure all four serving paths, cap concurrency, harden listeners"

# --- subject shape -------------------------------------------------------------
run "missing Risk trailer" 1 "refactor(proxy): unify pool serving pipelines"
run "two Risk trailers" 1 "fix(a): something

Risk: Low
Risk: High
Reviewed-By: x"
run "Risk inside a comment does not count" 1 "fix(a): something

# Risk: Low"
# Trailer *position*. A line is a trailer only when it is in the message's final
# paragraph. These two cases are the blind spot that let the old grep-the-text
# check pass a commit whose classification `git log %(trailers:key=Risk)` could
# not read -- including its `Reviewed-By:`, i.e. the independent-review record.
run "Risk demoted by a trailing paragraph" 1 "fix(a): something

Risk: High
Reviewed-By: auditor-context

This closing paragraph makes both lines above body prose."
run "Reviewed-By demoted by a trailing paragraph" 1 "feat(store): add lease heartbeat

Risk: High
Reviewed-By: auditor-context

A closing paragraph demotes the trailer block, so the review is unrecorded."
# Trailer KEYS are case-insensitive to git, so the gate has to be too. A `RISK:`
# that the existence check accepted but the value extraction missed skips the
# whole High branch -- the independent-review requirement dropped, one casing
# over. Both negative cases below exited 0 before that was fixed.
run "uppercase RISK routes to High" 1 "feat(store): add lease heartbeat

RISK: High"
run "uppercase REVIEWED-BY naming the author" 1 "feat(store): add lease heartbeat

Risk: High
REVIEWED-BY: $author_name"
run "uppercase keys with a valid reviewer" 0 "feat(store): add lease heartbeat

RISK: High
REVIEWED-BY: native-contract-auditor"
run "Risk value outside the four classes" 1 "fix(a): something

Risk: Medium"
run "Risk value with trailing junk" 1 "fix(a): something

Risk: Low (probably)"
run "empty subject (comments only)" 1 "# nothing here"
run "missing description after colon" 1 "feat: "
run "scope and breaking-change bang" 0 "feat(cli)!: drop --legacy flag

Risk: High
Reviewed-By: auditor-context"

# --- risk class routing --------------------------------------------------------
run "Risk: Low" 0 "fix(proxy): collapse the duplicated egress dispatch table into one

Risk: Low"
run "Risk: Normal" 0 "refactor(hub): give the Proxy Hub a service boundary

Risk: Normal"
# The positive control for the two cases above: prose in the body, with the
# classification in the final paragraph, is the normal shape and must pass.
run "body prose then a real trailer block" 0 "fix(proxy): collapse the duplicate dispatch table

The table was duplicated across two egress paths; one copy is now shared.

Risk: Normal"
run "Risk: High without Reviewed-By" 1 "feat(store): add lease heartbeat

Risk: High"
run "Risk: High with Reviewed-By" 0 "feat(store): add lease heartbeat

Risk: High
Reviewed-By: native-contract-auditor"
# Self-review. §1 calls independent review the single most important rule here,
# and naming yourself is the trivial way to satisfy its letter while breaking it.
run "Reviewed-By names the author" 1 "feat(store): add lease heartbeat

Risk: High
Reviewed-By: $author_name"
run "Risk: Architectural without ADR" 1 "feat(hub): add ring federation

Risk: Architectural
Reviewed-By: native-contract-auditor"
run "Risk: Architectural with ADR" 0 "feat(hub): add ring federation

Risk: Architectural
Reviewed-By: native-contract-auditor
ADR: adr/0023-ring-federation.md"

# --- not authored here ---------------------------------------------------------
run "merge commit" 0 "Merge branch 'main' into feature"
# git-authored subject shapes. `fixup!`/`squash!` are transient and are rewritten
# by `--autosquash`, so they are exempt like a merge. A revert is not: it lands.
run "fixup! commit" 0 "fixup! feat(cli): add models"
run "squash! commit" 0 "squash! feat(cli): add models"
run "git's generated revert subject" 1 "Revert \"feat(cli): add models\""
run "conventional revert subject" 0 "revert: feat(cli): add models

Risk: Low"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
