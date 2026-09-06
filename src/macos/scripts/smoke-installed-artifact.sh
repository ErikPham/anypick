#!/bin/bash
# Smoke-test an installed AnyPick artifact the way a clean user would meet it:
# sandboxed HOME/ANYPICK_HOME, restricted PATH, no source tree, no Node.
#
# Usage: smoke-installed-artifact.sh <AnyPick.app | anypick-core binary>
#
# Phase-4 exit criteria live here: launch, version, providers, a mutation
# round-trip (add/remove a compatible-vendor account), proxy start/stop, and
# a backup round-trip — every step asserting, nothing eyeballed.

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: smoke-installed-artifact.sh <AnyPick.app | anypick-core>" >&2
  exit 2
fi

target="$1"
if [[ "${target}" == *.app ]]; then
  for candidate in \
    "${target}/Contents/Helpers/anypick-core" \
    "${target}/Contents/MacOS/anypick-core"; do
    if [[ -x "${candidate}" ]]; then core="${candidate}"; break; fi
  done
  if [[ -z "${core:-}" ]]; then
    echo "error: no executable anypick-core inside ${target}" >&2
    exit 1
  fi
elif [[ -x "${target}" ]]; then
  core="${target}"
else
  echo "error: ${target} is neither an .app bundle nor an executable" >&2
  exit 1
fi

sandbox="$(mktemp -d "${TMPDIR:-/tmp}/anypick-smoke.XXXXXX")"
trap 'rm -rf "${sandbox}"' EXIT
export HOME="${sandbox}/home" ANYPICK_HOME="${sandbox}/data"
mkdir -p "${HOME}" "${ANYPICK_HOME}"
# A clean user has no toolchain on PATH: system binaries only.
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"

fail() { echo "smoke FAIL: $*" >&2; exit 1; }
pass() { echo "smoke ok: $*"; }

out="$("${core}" --version)" || fail "core --version exits $?"
[[ "${out}" == anypick-go* ]] || fail "unexpected --version: ${out}"
pass "version (${out})"

"${core}" providers >/dev/null || fail "providers"
pass "providers"

export ANYPICK_SECRET="sk-smoke-test-12345678"
"${core}" add account deepseek --name smoke --kind api-key >/dev/null \
  || fail "add account"
"${core}" account list | grep -q "deepseek/smoke" || fail "account list does not show the smoke account"
pass "add/list account"

"${core}" proxy enable deepseek smoke >/dev/null || fail "proxy enable"
"${core}" proxy start deepseek smoke >/dev/null || fail "proxy start"
"${core}" proxy stop deepseek smoke >/dev/null || fail "proxy stop"
pass "proxy start/stop"

"${core}" account export deepseek smoke --out "${sandbox}/smoke.json" >/dev/null \
  || fail "account export"
"${core}" account remove deepseek smoke --yes >/dev/null || fail "account remove"
"${core}" account import deepseek smoke "${sandbox}/smoke.json" >/dev/null \
  || fail "account import"
"${core}" account remove deepseek smoke --yes >/dev/null || fail "account remove (cleanup)"
pass "export/remove/import round-trip"

echo "smoke PASS: ${target}"
