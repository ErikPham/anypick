#!/usr/bin/env bash

# Headless Linux CLI gate (QA A–C): no TTY, unique ANYPICK_HOME, static amd64
# binary of the commit under test. Fail the job on any miss; always stop/kill
# leftovers in the EXIT trap.
#
# usage: linux-cli-headless.sh [BINARY]
# default BINARY: src/go/bin/anypick-linux-amd64 (built by the CI job)

set -euo pipefail

bin="${1:-src/go/bin/anypick-linux-amd64}"
if [[ ! -x "$bin" ]]; then
  echo "::error::missing executable binary: $bin" >&2
  exit 1
fi

work="$(mktemp -d "${RUNNER_TEMP:-/tmp}/anypick-linux-cli.XXXXXX")"
home="$work/home"
mkdir -p "$home"
export HOME="$home"
export ANYPICK_HOME="$home/.anypick"
# No display, no extra PATH entries — proves the static binary needs none.
export PATH=/usr/bin:/bin
unset DISPLAY WAYLAND_DISPLAY DBUS_SESSION_BUS_ADDRESS || true

run() {
  # stdin closed: isatty must be false.
  "$bin" "$@" </dev/null
}

cleanup() {
  set +e
  run stop >/dev/null 2>&1
  run serve --stop >/dev/null 2>&1
  # Kill any leftover under this home if stop failed closed.
  if [[ -f "$ANYPICK_HOME/runtime/supervisor/service.pid" ]]; then
    pid="$(python3 -c "import json,sys;print(json.load(open(sys.argv[1])).get('pid',0))" \
      "$ANYPICK_HOME/runtime/supervisor/service.pid" 2>/dev/null || true)"
    if [[ -n "${pid:-}" && "$pid" != "0" ]]; then
      kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
      sleep 0.2
      kill -KILL "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
    fi
  fi
  rm -rf "$work"
}
trap cleanup EXIT

fail() { echo "::error::$*" >&2; exit 1; }

echo "== A. No-TTY / help =="
out="$(run --version)" || fail "--version rc"
[[ -n "$out" ]] || fail "--version empty"
run doctor >/dev/null || fail "doctor rc"

set +e
tui_out="$(run tui 2>&1)"
tui_rc=$?
set -e
[[ "$tui_rc" -eq 2 ]] || fail "tui rc=$tui_rc want 2"
printf '%s' "$tui_out" | grep -q 'NO_TTY\|requires a terminal' || fail "tui missing NO_TTY: $tui_out"
[[ ! -f "$ANYPICK_HOME/runtime/supervisor/service.pid" ]] || fail "tui left service.pid"

set +e
help_out="$(run start --help 2>&1)"
help_rc=$?
set -e
[[ "$help_rc" -eq 0 ]] || fail "start --help rc=$help_rc want 0"
printf '%s' "$help_out" | grep -qi 'Usage' || fail "start --help missing Usage"
[[ ! -f "$ANYPICK_HOME/runtime/supervisor/service.pid" ]] || fail "start --help created service.pid"
pgrep -af "$bin.*_supervise" >/dev/null 2>&1 && fail "start --help left a supervisor" || true

set +e
serve_help="$(run serve --help 2>&1)"
serve_help_rc=$?
set -e
[[ "$serve_help_rc" -eq 0 ]] || fail "serve --help rc=$serve_help_rc want 0"
printf '%s' "$serve_help" | grep -qiE 'detach|Usage|port' || fail "serve --help missing text"
[[ ! -f "$ANYPICK_HOME/serve.pid" ]] || fail "serve --help left serve.pid"

echo "== B. Lifecycle =="
cycle() {
  local label="$1"
  out="$(run start)" || fail "$label start rc"
  pid="$(printf '%s' "$out" | sed -n 's/.*pid \([0-9][0-9]*\).*/\1/p' | head -1)"
  [[ -n "$pid" ]] || fail "$label start: no pid in: $out"
  [[ -d "/proc/$pid" ]] || fail "$label start: /proc/$pid missing"

  status="$(run status)" || fail "$label status rc"
  printf '%s' "$status" | grep -q "supervisor running (pid $pid)" || fail "$label status mismatch: $status"
  [[ -d "/proc/$pid" ]] || fail "$label status: process gone"

  run stop >/dev/null || fail "$label stop rc"
  status2="$(run status)" || fail "$label status-after-stop rc"
  printf '%s' "$status2" | grep -q 'not running' || fail "$label after stop: $status2"
  [[ ! -d "/proc/$pid" ]] || fail "$label stop left /proc/$pid"
  if [[ -f "$ANYPICK_HOME/runtime/supervisor/service.pid" ]]; then
    fail "$label stop left service.pid"
  fi
}

cycle first
cycle second

echo "== C. Serve path =="
serve_out="$(run serve --detach)" || fail "serve --detach rc"
port="$(printf '%s' "$serve_out" | sed -n 's|.*http://127\.0\.0\.1:\([0-9][0-9]*\).*|\1|p' | head -1)"
[[ -n "$port" ]] || fail "serve --detach: no port in: $serve_out"
# Bound to loopback only.
if command -v ss >/dev/null; then
  ss -ltn | grep -E "127\.0\.0\.1:$port" >/dev/null || fail "serve not listening on 127.0.0.1:$port"
  if ss -ltn | grep -E "0\.0\.0\.0:$port|::$port|\*:$port" >/dev/null; then
    fail "serve listening beyond loopback on $port"
  fi
fi
run serve --stop >/dev/null || fail "serve --stop rc"
if command -v ss >/dev/null; then
  ss -ltn | grep -E ":$port\\b" >/dev/null && fail "serve --stop left port $port open" || true
fi

echo "smoke ok: linux CLI headless (QA A–C)"
