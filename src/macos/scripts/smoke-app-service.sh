#!/bin/sh
set -eu

app_path=${1:?usage: smoke-app-service.sh /path/to/AnyPick.app}
test -x "$app_path/Contents/MacOS/AnyPick"
test -x "$app_path/Contents/Helpers/anypick-core"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
"$script_dir/check-node-free-app.sh" "$app_path"

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/anypick-app-smoke.XXXXXX")
smoke_pid=''
cleanup() {
  if [ -n "$smoke_pid" ] && kill -0 "$smoke_pid" 2>/dev/null; then
    kill "$smoke_pid" 2>/dev/null || true
  fi
  rm -rf "$smoke_root"
}
trap cleanup EXIT INT TERM

mkdir -p "$smoke_root/home" "$smoke_root/data"
marker="$smoke_root/result"
log="$smoke_root/stderr.log"
HOME="$smoke_root/home" \
ANYPICK_HOME="$smoke_root/data" \
ANYPICK_APP_SERVICE_SMOKE_PATH="$marker" \
  "$app_path/Contents/MacOS/AnyPick" 2>"$log" &
smoke_pid=$!

attempt=0
while [ "$attempt" -lt 200 ] && [ ! -f "$marker" ]; do
  if ! kill -0 "$smoke_pid" 2>/dev/null; then
    wait "$smoke_pid" || true
    sed -n '1,120p' "$log" >&2
    echo 'AnyPick exited before the app-service smoke completed.' >&2
    exit 1
  fi
  sleep 0.05
  attempt=$((attempt + 1))
done

if [ ! -f "$marker" ] || [ "$(tr -d '\r\n' < "$marker")" != 'ready' ]; then
  test ! -f "$marker" || cat "$marker" >&2
  sed -n '1,120p' "$log" >&2
  echo 'AnyPick did not reach a negotiated snapshot.' >&2
  exit 1
fi

wait "$smoke_pid"
smoke_pid=''
echo 'AnyPick app-owned service smoke passed.'
