#!/bin/bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Two levels up, not three: this script lives at macos/scripts, so
# ../.. is the repository root. It used to be src/macos/scripts, where the
# extra level was correct — the path was not adjusted when the tree moved.
repo_root="$(cd "${script_dir}/../.." && pwd)"
go_root="${repo_root}/go"
destination_dir="${TARGET_BUILD_DIR:?}/${CONTENTS_FOLDER_PATH:?}/Helpers"
destination="${destination_dir}/anypick-core"
resources_dir="${TARGET_BUILD_DIR:?}/${UNLOCALIZED_RESOURCES_FOLDER_PATH:?}"
derived_dir="${DERIVED_FILE_DIR:-${TARGET_TEMP_DIR:?}}"
mkdir -p "${derived_dir}"
scratch="$(mktemp -d "${derived_dir}/anypick-core.XXXXXX")"
trap 'rm -rf "${scratch}"' EXIT

go_binary="${GO_BINARY:-$(command -v go || true)}"
if [[ -z "${go_binary}" || ! -x "${go_binary}" ]]; then
  echo "error: a Go launcher is required on the build machine to bundle anypick-core." >&2
  exit 1
fi
go_toolchain="$(/usr/bin/awk '$1 == "toolchain" { print $2; exit }' "${go_root}/go.mod")"
if [[ -z "${go_toolchain}" ]]; then
  echo "error: go/go.mod must pin the helper build toolchain." >&2
  exit 1
fi

# The ambient GOPATH on some machines points at another project's module
# cache, which makes the build below stall in module resolution and look like
# a hang. Pin the cache explicitly; GOMODCACHE can still be overridden from
# the environment or an Xcode build setting.
export GOMODCACHE="${GOMODCACHE:-${HOME}/go/pkg/mod}"
echo "note: building anypick-core with GOMODCACHE=${GOMODCACHE}" >&2

requested_archs="${ARCHS:-${CURRENT_ARCH:-${NATIVE_ARCH_ACTUAL:-$(uname -m)}}}"
normalized_archs=()
for architecture in ${requested_archs}; do
  case "${architecture}" in
    arm64)
      normalized_archs+=("arm64")
      ;;
    x86_64|amd64)
      normalized_archs+=("x86_64")
      ;;
    *)
      echo "error: unsupported macOS helper architecture: ${architecture}" >&2
      exit 1
      ;;
  esac
done

if [[ ${#normalized_archs[@]} -eq 0 ]]; then
  echo "error: Xcode did not provide a helper architecture." >&2
  exit 1
fi

# Keep universal slice order stable regardless of the order Xcode supplies.
ordered_archs=()
for wanted in arm64 x86_64; do
  for architecture in "${normalized_archs[@]}"; do
    if [[ "${architecture}" == "${wanted}" ]]; then
      ordered_archs+=("${wanted}")
      break
    fi
  done
done

mkdir -p "${derived_dir}/go-build-cache" "${destination_dir}"
binaries=()
for architecture in "${ordered_archs[@]}"; do
  go_arch="${architecture}"
  if [[ "${architecture}" == "x86_64" ]]; then
    go_arch="amd64"
  fi
  output="${scratch}/anypick-core-${architecture}"
  (
    cd "${go_root}"
    env \
      CGO_ENABLED=0 \
      GOARCH="${go_arch}" \
      GOCACHE="${derived_dir}/go-build-cache" \
      GOMODCACHE="${GOMODCACHE:-${HOME}/go/pkg/mod}" \
      GOOS=darwin \
      GOTOOLCHAIN="${go_toolchain}" \
      "${go_binary}" build \
        -buildvcs=false \
        -mod=readonly \
        -trimpath \
        -ldflags='-s -w -buildid=' \
        -o "${output}" \
        ./cmd/anypick
  )
  binaries+=("${output}")
done

unsigned="${scratch}/anypick-core"
if [[ ${#binaries[@]} -eq 1 ]]; then
  cp "${binaries[0]}" "${unsigned}"
else
  /usr/bin/lipo -create "${binaries[@]}" -output "${unsigned}"
fi
/usr/bin/install -m 0755 "${unsigned}" "${destination}"

# ADR 0020: native apps ship Go first-party/compatible providers only.
# Never copy plugin-host.mjs, a private Node runtime, or ESM plugin packs.
/bin/rm -rf "${resources_dir}/plugin-host" "${resources_dir}/plugins"

# Nested code must be signed before Xcode signs the outer app bundle. Local
# builds use ad-hoc signing; archives inherit the selected Developer ID identity.
if [[ "${CODE_SIGNING_ALLOWED:-NO}" == "YES" ]]; then
  identity="${EXPANDED_CODE_SIGN_IDENTITY:--}"
  sign_args=(--force --sign "${identity}" --options runtime)
  if [[ "${CONFIGURATION:-Debug}" == "Release" && "${identity}" != "-" ]]; then
    sign_args+=(--timestamp)
  else
    sign_args+=(--timestamp=none)
  fi
  /usr/bin/codesign "${sign_args[@]}" "${destination}"
fi

/usr/bin/lipo -archs "${destination}"

app_bundle="${TARGET_BUILD_DIR}/${FULL_PRODUCT_NAME:-AnyPick.app}"
# Full ADR 0020 contract (names, shebang, path leaks, symlink escape) — not
# a filename-only check. Cross-tested against the Go scanner in
# go/internal/artifact, which rejects the same fixtures.
/bin/bash "${script_dir}/check-node-free-app.sh" "${app_bundle}"
