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
run "Risk: High without Reviewed-By" 1 "feat(store): add lease heartbeat

Risk: High"
run "Risk: High with Reviewed-By" 0 "feat(store): add lease heartbeat

Risk: High
Reviewed-By: native-contract-auditor"
run "Risk: Architectural without ADR" 1 "feat(hub): add ring federation

Risk: Architectural
Reviewed-By: native-contract-auditor"
run "Risk: Architectural with ADR" 0 "feat(hub): add ring federation

Risk: Architectural
Reviewed-By: native-contract-auditor
ADR: adr/0023-ring-federation.md"

# --- not authored here ---------------------------------------------------------
run "merge commit" 0 "Merge branch 'main' into feature"

printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
