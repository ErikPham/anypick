#!/usr/bin/env bash

# Update a separately owned homebrew-* tap after a GitHub Release exists.
# This is opt-in in the release workflow because the tap repository and token
# are deployment configuration, not source code in AnyPick.

set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 VERSION RELEASE_BASE_URL SHA256SUMS" >&2
  exit 2
fi

: "${HOMEBREW_TAP_TOKEN:?HOMEBREW_TAP_TOKEN is required}"
tap_repository="${HOMEBREW_TAP_REPOSITORY:-outof0/homebrew-tap}"
version="$1"
release_base="$2"
sums="$3"

checkout="$(mktemp -d "${RUNNER_TEMP:-/tmp}/anypick-homebrew.XXXXXX")"
trap 'rm -rf "$checkout"' EXIT

auth_header="AUTHORIZATION: bearer ${HOMEBREW_TAP_TOKEN}"
git -c http.extraheader="$auth_header" clone --depth=1 "https://github.com/${tap_repository}.git" "$checkout/tap"
bash "$(dirname "$0")/render.sh" "$version" "$release_base" "$sums" "$checkout/tap"

cd "$checkout/tap"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git add Formula/anypick.rb Casks/anypick.rb
if git diff --cached --quiet; then
  echo "Homebrew tap already contains AnyPick ${version}."
else
  git diff --cached --check
  git commit -m "anypick ${version}"
  git -c http.extraheader="$auth_header" push origin HEAD
fi
