#!/usr/bin/env bash
# Single local entry for the Go engine.
#
# Replaces scripts/dev-go.mjs: the repository no longer depends on a JavaScript
# runtime, and a build-and-run wrapper has no business needing one.
#
#   scripts/dev.sh <command> [args...]   run the built binary
#   scripts/dev.sh --sandbox <command>   same, against a throwaway HOME/data root
#   scripts/dev.sh --launch-app          start the desktop supervisor detached
#   scripts/dev.sh --stop-app            stop it
#
# All runs use an isolated data root unless --sandbox is given, because account
# and configuration mutations touch real developer credentials.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_DIR="$ROOT/go"
BIN="$GO_DIR/bin/anypick-dev"
PID_FILE="${TMPDIR:-/tmp}/anypick-go-dev.pid"
LOG_FILE="${TMPDIR:-/tmp}/anypick-go-dev.log"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "Go not found on PATH. See go/go.mod for the pinned version."

# ── flags ────────────────────────────────────────────────────────────
SANDBOX=0
case "${1:-}" in
  --sandbox)    SANDBOX=1; shift ;;
  --launch-app) exec_flag=launch; shift ;;
  --stop-app)   exec_flag=stop; shift ;;
  *)            exec_flag=run ;;
esac
exec_flag="${exec_flag:-run}"

# ── build ────────────────────────────────────────────────────────────
mkdir -p "$GO_DIR/bin"
(cd "$GO_DIR" && GOTOOLCHAIN=auto go build -o "$BIN" ./cmd/anypick) ||
  die "build failed — see compile errors above."

# ── environment ──────────────────────────────────────────────────────
export GOTOOLCHAIN=auto

if [ "$SANDBOX" -eq 1 ]; then
  BASE="${TMPDIR:-/tmp}/anypick-go-sandbox"
  export HOME="$BASE/home"
  export ANYPICK_HOME="$BASE/data"
  mkdir -p "$HOME" "$ANYPICK_HOME"
  # Seed a fake signed-in Codex once so `add codex` works out of the box.
  CODEX_AUTH="$HOME/.codex/auth.json"
  if [ ! -f "$CODEX_AUTH" ]; then
    mkdir -p "$HOME/.codex"
    HEADER='eyJhbGciOiJub25lIn0'
    PAYLOAD="$(printf '%s' '{"email":"me@example.com"}' | base64 | tr -d '\n' | tr '+/' '-_' | tr -d '=')"
    printf '%s.%s.' "$HEADER" "$PAYLOAD" > "$CODEX_AUTH"
    chmod 600 "$CODEX_AUTH"
  fi
fi

pid_alive() { kill -0 "$1" 2>/dev/null; }

supervisor_pid_path() { printf '%s' "$PID_FILE"; }

native_app_binary() {
  # Prefer a built app bundle; fall back to whatever the repo produced.
  for candidate in \
    "$ROOT/macos/build/Build/Products/Debug/AnyPick.app/Contents/MacOS/AnyPick" \
    "$ROOT/macos/build/Build/Products/Release/AnyPick.app/Contents/MacOS/AnyPick"; do
    [ -x "$candidate" ] && { printf '%s' "$candidate"; return 0; }
  done
  return 1
}

launch_desktop() {
  if [ -f "$PID_FILE" ] && pid_alive "$(cat "$PID_FILE" 2>/dev/null || echo 0)"; then
    echo "[go:dev] already running (pid $(cat "$PID_FILE"))"
    return 0
  fi
  [ -f "$PID_FILE" ] && rm -f "$PID_FILE"

  APP_BIN="$(native_app_binary || true)"
  if [ -z "${APP_BIN:-}" ]; then
    echo "[go:dev] no built app bundle found."
    echo "       Build it in Xcode first: open macos/AnyPick.xcodeproj"
    exit 1
  fi
  export ANYPICK_APP_BIN="$APP_BIN"

  "$BIN" app run >> "$LOG_FILE" 2>&1 &
  CHILD=$!
  echo "$CHILD" > "$PID_FILE"
  chmod 600 "$PID_FILE"

  # Fail fast if the supervisor died during startup instead of reporting an
  # app that instantly vanished.
  sleep 1.2
  if ! pid_alive "$CHILD"; then
    echo "[go:dev] supervisor exited during startup — log: $LOG_FILE" >&2
    exit 1
  fi
  echo "[go:dev] started (pid $CHILD)"
  echo "[go:dev] log: $LOG_FILE"
  echo "[go:dev] stop with: scripts/dev.sh --stop-app"
}

stop_desktop() {
  if [ ! -f "$PID_FILE" ]; then
    echo "[go:dev] not running"
    return 0
  fi
  PID="$(cat "$PID_FILE" 2>/dev/null || echo 0)"
  if ! pid_alive "$PID"; then
    rm -f "$PID_FILE"
    echo "[go:dev] not running"
    return 0
  fi
  # The supervisor leads its own process group; signal the group so the app
  # and any helpers die with it.
  kill -TERM -"$PID" 2>/dev/null || kill -TERM "$PID" 2>/dev/null || true
  rm -f "$PID_FILE"
  echo "[go:dev] stopped (pid $PID)"
}

case "$exec_flag" in
  launch) launch_desktop ;;
  stop)   stop_desktop ;;
  run)
    if [ "$SANDBOX" -eq 0 ] && [ "$#" -eq 0 ]; then
      echo "usage: scripts/dev.sh <command> · scripts/dev.sh --sandbox <command> · scripts/dev.sh --launch-app" >&2
      exit 2
    fi
    exec "$BIN" "$@"
    ;;
esac
