#!/usr/bin/env bash

# Render the three manifests required for a Windows Package Manager
# submission. The output can be submitted to microsoft/winget-pkgs or kept as
# a release asset for review before submission.

set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 VERSION RELEASE_BASE_URL SHA256SUMS OUTPUT_DIR" >&2
  exit 2
fi

version="$1"
release_base="$2"
sums="$3"
output="$4"
asset="AnyPick-${version}-windows-x64.msi"

sha="$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums")"
if [[ ! "$sha" =~ ^[[:xdigit:]]{64}$ ]]; then
  echo "missing checksum for $asset" >&2
  exit 1
fi

mkdir -p "$output"

cat > "$output/outof0.AnyPick.yaml" <<EOF
PackageIdentifier: outof0.AnyPick
PackageVersion: ${version}
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.10.0
EOF

cat > "$output/outof0.AnyPick.locale.en-US.yaml" <<EOF
PackageIdentifier: outof0.AnyPick
PackageVersion: ${version}
PackageLocale: en-US
Publisher: outof0
PublisherUrl: https://github.com/outof0/anypick
PublisherSupportUrl: https://github.com/outof0/anypick/issues
Author: outof0
PackageName: AnyPick
PackageUrl: https://github.com/outof0/anypick
License: MIT
ShortDescription: Native account and proxy manager for AI coding clients
ManifestType: defaultLocale
ManifestVersion: 1.10.0
EOF

cat > "$output/outof0.AnyPick.installer.yaml" <<EOF
PackageIdentifier: outof0.AnyPick
PackageVersion: ${version}
InstallerLocale: en-US
Installers:
- Architecture: x64
  InstallerType: msi
  InstallerUrl: ${release_base}/${asset}
  InstallerSha256: ${sha}
ManifestType: installer
ManifestVersion: 1.10.0
EOF
