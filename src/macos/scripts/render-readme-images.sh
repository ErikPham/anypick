#!/usr/bin/env bash
# Renders the README screenshots from the fixture, off-screen: the app never
# activates or shows a window, and each PNG holds only AnyPick's own window.
# Usage: src/macos/scripts/render-readme-images.sh [path/to/AnyPick.app]
set -euo pipefail

root="$(cd "$(dirname "$0")/../../.." && pwd)"
app="${1:-}"
if [[ -z "$app" ]]; then
  derived="${DERIVED_DATA_DIR:-$HOME/Library/Developer/Xcode/DerivedData}"
  app="$(find "$derived" -path '*/Build/Products/Debug/AnyPick.app' -print -quit)"
fi
binary="$app/Contents/MacOS/AnyPick"
[[ -x "$binary" ]] || { echo "no AnyPick binary at $binary (build-macos first)" >&2; exit 1; }

out="$root/docs/public/screenshots"
mkdir -p "$out"
size="${ANYPICK_MACOS_RENDER_SIZE:-980x680}"

# name appearance destination flow
shots=(
  "overview-light light overview -"
  "overview-dark dark overview -"
  "apps light apps -"
  "accounts light accounts -"
  "add-login light accounts add-account"
  "proxies light proxies -"
  "activity light activity -"
)

for shot in "${shots[@]}"; do
  read -r name appearance destination flow <<<"$shot"
  png="$out/$name.png"
  rm -f "$png"
  env -i HOME="$HOME" PATH="/usr/bin:/bin" \
    ANYPICK_MACOS_FIXTURE=ready \
    ANYPICK_MACOS_APPEARANCE="$appearance" \
    ANYPICK_MACOS_DESTINATION="$destination" \
    ${flow:+$([[ "$flow" != - ]] && echo "ANYPICK_MACOS_FLOW=$flow")} \
    ANYPICK_MACOS_RENDER_SIZE="$size" \
    ANYPICK_MACOS_RENDER_DELAY_MS=1500 \
    ANYPICK_MACOS_RENDER_PATH="$png" \
    "$binary"
  echo "$png"
done
