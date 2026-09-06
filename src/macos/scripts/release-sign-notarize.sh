#!/bin/bash
# Build, Developer ID sign, notarize and staple the universal macOS app.
#
# ADR 0020: a public macOS artifact is signed, notarized and stapled. This
# script is the single place that promise is kept — the release workflow
# fails before it when the signing/notarization secrets are absent, so an
# unsigned build can never reach a public Release.
#
# Required environment:
#   APPLE_DEVELOPER_ID_APPLICATION  "Developer ID Application: <Team> (<ID>)"
#   APPLE_API_KEY_P8                contents of the App Store Connect API .p8 key
#   APPLE_API_KEY_ID                the .p8 key ID
#   APPLE_API_ISSUER_ID             the App Store Connect issuer ID
#
# Usage: release-sign-notarize.sh [--version 1.2.3]

set -euo pipefail

version=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    *) echo "error: unknown argument $1" >&2; exit 1 ;;
  esac
done

: "${APPLE_DEVELOPER_ID_APPLICATION:?APPLE_DEVELOPER_ID_APPLICATION is required (ADR 0020).}"
: "${APPLE_API_KEY_P8:?APPLE_API_KEY_P8 is required for notarization.}"
: "${APPLE_API_KEY_ID:?APPLE_API_KEY_ID is required for notarization.}"
: "${APPLE_API_ISSUER_ID:?APPLE_API_ISSUER_ID is required for notarization.}"

xcodebuild -project src/macos/AnyPick.xcodeproj -scheme AnyPick \
  -configuration Release -destination 'generic/platform=macOS' \
  ARCHS='arm64 x86_64' ONLY_ACTIVE_ARCH=NO \
  CODE_SIGN_STYLE=Manual \
  CODE_SIGN_IDENTITY="${APPLE_DEVELOPER_ID_APPLICATION}" \
  DEVELOPMENT_TEAM="$(sed -E 's/.*\(([A-Z0-9]+)\)/\1/' <<< "${APPLE_DEVELOPER_ID_APPLICATION}")" \
  build

app="$(find "$HOME/Library/Developer/Xcode/DerivedData" -path '*/Build/Products/Release/AnyPick.app' -print -quit)"
if [[ -z "${app}" ]]; then
  echo "error: signed build produced no AnyPick.app under DerivedData." >&2
  exit 1
fi

# The nested helper must carry a valid signature too, or Gatekeeper rejects
# the bundle at first launch on a clean machine.
codesign --verify --deep --strict "${app}"

keyfile="$(mktemp -d)/AuthKey.p8"
trap 'rm -rf "$(dirname "${keyfile}")"' EXIT
printf '%s' "${APPLE_API_KEY_P8}" > "${keyfile}"

zipfile="$(mktemp -d)/AnyPick-notarize.zip"
ditto -c -k --sequesterRsrc --keepParent "${app}" "${zipfile}"

xcrun notarytool submit "${zipfile}" \
  --key "${keyfile}" \
  --key-id "${APPLE_API_KEY_ID}" \
  --issuer "${APPLE_API_ISSUER_ID}" \
  --wait

xcrun stapler staple "${app}"
xcrun stapler validate "${app}"

# Gatekeeper assessment runs AFTER the ticket is stapled: assessing before
# notarization can only report the pre-notarization state.
spctl -a -t exec -vv "${app}"

# Universal-architecture gate: both slices must be present.
if ! lipo -archs "${app}/Contents/MacOS/AnyPick" | grep -q "arm64" || \
   ! lipo -archs "${app}/Contents/MacOS/AnyPick" | grep -q "x86_64"; then
  echo "error: ${app} is not a universal binary." >&2
  exit 1
fi

echo "signed, notarized and stapled: ${app}"
if [[ -n "${version}" ]]; then
  echo "version: ${version}"
fi
