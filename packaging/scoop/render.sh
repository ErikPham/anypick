#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 VERSION RELEASE_BASE_URL SHA256SUMS OUTPUT_DIR" >&2
  exit 2
fi

version="$1"
release_base="$2"
sums="$3"
output="$4"

checksum_for() {
  local asset="$1"
  awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums"
}

x64_asset="anypick-${version}-windows-amd64.zip"
arm64_asset="anypick-${version}-windows-arm64.zip"
x64_sha="$(checksum_for "$x64_asset")"
arm64_sha="$(checksum_for "$arm64_asset")"
for checksum in "$x64_sha" "$arm64_sha"; do
  [[ "$checksum" =~ ^[[:xdigit:]]{64}$ ]] || { echo "missing Scoop checksum" >&2; exit 1; }
done

mkdir -p "$output"
cat > "$output/anypick.json" <<EOF
{
  "version": "${version}",
  "description": "Native account and proxy manager for AI coding clients",
  "homepage": "https://github.com/outof0/anypick",
  "license": "MIT",
  "architecture": {
    "64bit": {
      "url": "${release_base}/${x64_asset}",
      "hash": "${x64_sha}"
    },
    "arm64": {
      "url": "${release_base}/${arm64_asset}",
      "hash": "${arm64_sha}"
    }
  },
  "bin": "anypick.exe",
  "checkver": {
    "github": "https://github.com/outof0/anypick"
  },
  "autoupdate": {
    "architecture": {
      "64bit": {
        "url": "https://github.com/outof0/anypick/releases/download/v\$version/anypick-\$version-windows-amd64.zip"
      },
      "arm64": {
        "url": "https://github.com/outof0/anypick/releases/download/v\$version/anypick-\$version-windows-arm64.zip"
      }
    }
  }
}
EOF
