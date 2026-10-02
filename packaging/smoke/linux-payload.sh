#!/usr/bin/env bash

# Target-OS payload smoke for the staged Linux desktop bundles. Each archive is
# extracted without installing anything, the bundled Go helper is located, and
# it must start and answer with the planned version. The GUI itself is not
# launched (the runner has no display): this is the installer-payload gate that
# release-scope=all publishes behind.
#
# usage: linux-payload.sh STAGE_DIR VERSION

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 STAGE_DIR VERSION" >&2
  exit 2
fi

stage="$1"
version="$2"
work="$(mktemp -d "${RUNNER_TEMP:-/tmp}/anypick-linux-smoke.XXXXXX")"
trap 'rm -rf "$work"' EXIT

smoke_helper() {
  local helper="$1" label="$2" home out
  if [[ ! -f "$helper" ]]; then
    echo "::error::$label payload has no anypick-core helper" >&2
    find "$work" -type f | head -60 >&2 || true
    return 1
  fi
  chmod +x "$helper" 2>/dev/null || true
  home="$(mktemp -d "$work/home.XXXXXX")"
  out="$(env -i HOME="$home" ANYPICK_HOME="$home/.anypick" PATH=/usr/bin:/bin "$helper" --version)"
  if [[ "$out" != "anypick-go $version" ]]; then
    echo "::error::$label helper reports '$out', want 'anypick-go $version'" >&2
    return 1
  fi
  env -i HOME="$home" ANYPICK_HOME="$home/.anypick" PATH=/usr/bin:/bin "$helper" providers >/dev/null
  echo "smoke ok: $label helper ($out)"
}

scan_node_free() {
  local root="$1"
  if [[ -z "${GITHUB_WORKSPACE:-}" || ! -d "$GITHUB_WORKSPACE/src/go" ]]; then
    return 0
  fi
  ( cd "$GITHUB_WORKSPACE" && GOPATH="${GOPATH:-$HOME/go}" GOTOOLCHAIN=auto \
      go run ./src/go/cmd/anypick-artifact node-free "$root" )
}

smoked=0
for deb in "$stage"/*.deb; do
  [[ -e "$deb" ]] || continue
  dir="$work/deb"
  mkdir -p "$dir"
  dpkg-deb -x "$deb" "$dir"
  helper="$(find "$dir" -type f -name 'anypick-core' -print -quit)"
  smoke_helper "$helper" "deb"
  scan_node_free "$dir"
  smoked=1
done

for image in "$stage"/*.AppImage; do
  [[ -e "$image" ]] || continue
  chmod +x "$image"
  ( cd "$work" && "$image" --appimage-extract >/dev/null )
  helper="$(find "$work/squashfs-root" -type f -name 'anypick-core' -print -quit)"
  smoke_helper "$helper" "AppImage"
  scan_node_free "$work/squashfs-root"
  smoked=1
done

for rpm in "$stage"/*.rpm; do
  [[ -e "$rpm" ]] || continue
  dir="$work/rpm"
  mkdir -p "$dir"
  if command -v rpm2archive >/dev/null; then
    ( cd "$dir" && rpm2archive -n "$rpm" | tar -xz )
  elif command -v rpm2cpio >/dev/null && command -v cpio >/dev/null; then
    ( cd "$dir" && rpm2cpio "$rpm" | cpio -idm --quiet )
  else
    echo "notice: rpm2archive/rpm2cpio unavailable; skipping the rpm payload smoke"
    continue
  fi
  helper="$(find "$dir" -type f -name 'anypick-core' -print -quit)"
  smoke_helper "$helper" "rpm"
  scan_node_free "$dir"
  smoked=1
done

if [[ "$smoked" -eq 0 ]]; then
  echo "::error::no Linux desktop bundle was smoke-tested in $stage" >&2
  exit 1
fi
