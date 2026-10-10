#!/bin/bash

# Build a universal release app and apply an ad-hoc code signature. macOS
# users approve the first launch because the app is not notarized.
set -euo pipefail

if [[ $# -ne 2 || "$1" != "--version" || ! "$2" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: $0 --version x.y.z" >&2
  exit 2
fi

version="$2"
derived_dir="${DERIVED_DATA_DIR:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/anypick-macos-derived}"

GOPATH="${HOME}/go" GOTOOLCHAIN=auto ANYPICK_GO_VERSION="$version" \
  xcodebuild -project src/macos/AnyPick.xcodeproj -scheme AnyPick \
    -configuration Release -destination 'generic/platform=macOS' \
    -derivedDataPath "$derived_dir" ARCHS='arm64 x86_64' \
    ONLY_ACTIVE_ARCH=NO CODE_SIGNING_ALLOWED=NO build

app="$derived_dir/Build/Products/Release/AnyPick.app"
if [[ ! -d "$app" ]]; then
  echo "error: release build produced no AnyPick.app at $app" >&2
  exit 1
fi

codesign --force --deep -s - "$app"
codesign --verify --deep --strict "$app"

architectures="$(lipo -archs "$app/Contents/MacOS/AnyPick")"
grep -qw arm64 <<<"$architectures" || {
  echo "error: AnyPick.app is missing its arm64 slice" >&2
  exit 1
}
grep -qw x86_64 <<<"$architectures" || {
  echo "error: AnyPick.app is missing its x86_64 slice" >&2
  exit 1
}

echo "ad-hoc macOS release app: $app"
