#!/usr/bin/env bash

# Render the formula and cask that belong in an external homebrew-* tap.
# Keeping this generator in the source tree makes the release workflow
# deterministic without making the main repository pretend to be a tap.

set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 VERSION RELEASE_BASE_URL SHA256SUMS TAP_ROOT" >&2
  exit 2
fi

version="$1"
release_base="$2"
sums="$3"
tap_root="$4"

checksum_for() {
  local asset="$1"
  awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums"
}

require_checksum() {
  local asset="$1"
  local checksum
  checksum="$(checksum_for "$asset")"
  if [[ ! "$checksum" =~ ^[[:xdigit:]]{64}$ ]]; then
    echo "missing checksum for $asset" >&2
    exit 1
  fi
  printf '%s' "$checksum"
}

formula_dir="$tap_root/Formula"
cask_dir="$tap_root/Casks"
mkdir -p "$formula_dir" "$cask_dir"

mac_arm_asset="anypick-${version}-darwin-arm64.tar.gz"
mac_intel_asset="anypick-${version}-darwin-amd64.tar.gz"
linux_arm_asset="anypick-${version}-linux-arm64.tar.gz"
linux_intel_asset="anypick-${version}-linux-amd64.tar.gz"
app_asset="AnyPick-macos-universal.zip"

mac_arm_sha="$(require_checksum "$mac_arm_asset")"
mac_intel_sha="$(require_checksum "$mac_intel_asset")"
linux_arm_sha="$(require_checksum "$linux_arm_asset")"
linux_intel_sha="$(require_checksum "$linux_intel_asset")"
app_sha="$(require_checksum "$app_asset")"

cat > "$formula_dir/anypick.rb" <<EOF
class Anypick < Formula
  desc "Native account and proxy manager for AI coding clients"
  homepage "https://github.com/outof0/anypick"
  version "${version}"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "${release_base}/${mac_arm_asset}"
      sha256 "${mac_arm_sha}"
    else
      url "${release_base}/${mac_intel_asset}"
      sha256 "${mac_intel_sha}"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "${release_base}/${linux_arm_asset}"
      sha256 "${linux_arm_sha}"
    else
      url "${release_base}/${linux_intel_asset}"
      sha256 "${linux_intel_sha}"
    end
  end

  def install
    bin.install "anypick"
  end

  test do
    system bin/"anypick", "--version"
  end
end
EOF

cat > "$cask_dir/anypick.rb" <<EOF
cask "anypick" do
  version "${version}"
  sha256 "${app_sha}"

  url "${release_base}/${app_asset}"
  name "AnyPick"
  desc "Native account and proxy manager for AI coding clients"
  homepage "https://github.com/outof0/anypick"

  app "AnyPick.app"
end
EOF

ruby -c "$formula_dir/anypick.rb" >/dev/null
ruby -c "$cask_dir/anypick.rb" >/dev/null
