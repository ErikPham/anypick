#!/bin/bash
# ADR 0020 production artifact gate (Xcode bundle, archive, smoke, CI).
# Contract matches scripts/check-node-free-artifact.mjs. Shared fixtures live
# in scripts/node-free-artifact-fixtures.mjs; both scanners must fail every
# malicious fixture and pass the clean helper bundle.
#
# Content scan bound: 512 KiB (MAX_TEXT_BYTES). Files at or under that size
# are grepped in full. Larger files still fail on name, symlink, and shebang.
set -euo pipefail

MAX_TEXT_BYTES=$((512 * 1024))

app_path=${1:?usage: check-node-free-app.sh <artifact-root>}

if [[ ! -d "${app_path}" ]]; then
  echo "error: ${app_path} is not a directory." >&2
  exit 1
fi

root="$(cd "${app_path}" && pwd -P)"
violations=()

lower() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

is_inside() {
  case "$1" in
    "${root}" | "${root}"/*) return 0 ;;
    *) return 1 ;;
  esac
}

has_nul() {
  LC_ALL=C tr -d -c '\0' <"$1" | grep -q .
}

record() {
  violations+=("$1")
}

while IFS= read -r -d '' path; do
  name="$(basename "${path}")"
  lowered="$(lower "${name}")"
  rel="${path#"${root}"/}"
  if [[ "${path}" == "${root}" ]]; then
    rel="."
  fi

  case "${lowered}" in
    node | node.exe | npm | npm.cmd | npm.ps1 | npx | npx.cmd | pnpm | pnpm.cjs | pnpm.cmd | pnpm.exe | tsx | tsx.cmd | plugin-host.mjs | plugin-host.js)
      record "forbidden name ${rel}"
      ;;
  esac
  case "${lowered}" in
    *.mjs | *.cjs) record "forbidden Node module ${rel}" ;;
  esac
  if [[ "${lowered}" == "plugin-host" ]]; then
    record "forbidden plugin-host path ${rel}"
  fi

  if [[ -L "${path}" ]]; then
    if [[ ! -e "${path}" ]]; then
      record "dangling symlink ${rel}"
      continue
    fi
    target="$(realpath "${path}")"
    if ! is_inside "${target}"; then
      record "symlink ${rel} escapes the artifact"
    fi
    continue
  fi

  if [[ -d "${path}" || ! -f "${path}" ]]; then
    continue
  fi

  size="$(wc -c <"${path}" | tr -d ' ')"
  if [[ "${size}" -eq 0 ]]; then
    continue
  fi

  # The whole first line, not a 256-byte prefix. dd stopped at byte 256, so an
  # interpreter past that offset was invisible to this scanner while
  # hasNodeShebang in nodefree.go saw it — the same artifact passed Xcode and
  # failed CI (CODE_REVIEW.md §5). read consumes one line regardless of length,
  # which is the same bound the Go scanner's IndexByte uses. read reports EOF
  # for a last line with no newline; that is not a violation of its own.
  first_line=""
  IFS= read -r first_line <"${path}" || true
  if [[ "${first_line}" == '#!'* ]] &&
    printf '%s' "${first_line}" | grep -qiE '(^|[^[:alnum:]_])node(exe)?([^[:alnum:]_]|$)'; then
    record "Node shebang ${rel}"
  fi

  if [[ "${size}" -gt "${MAX_TEXT_BYTES}" ]]; then
    continue
  fi
  if has_nul "${path}"; then
    continue
  fi
  # [[:space:]] is the boundary class shared with devPathPattern in
  # nodefree.go. It matches the vertical tab, which RE2's \s does not, so the
  # two scanners disagreed on the byte after the path prefix; the Go side was
  # changed to this class rather than the reverse because POSIX is the only
  # spelling both engines share.
  if grep -E -q '(^|[[:space:]"'\''=])(/Users/|/home/|/opt/homebrew/opt/node)' "${path}" ||
    grep -E -q '(^|[[:space:]"'\''=])(GOROOT=|GOPATH=)' "${path}"; then
    record "developer path leaked in ${rel}"
  fi
done < <(find -P "${root}" -print0)

if [[ ${#violations[@]} -gt 0 ]]; then
  echo "ADR 0020: ${app_path} is not a Node-free native artifact:" >&2
  printf '  - %s\n' "${violations[@]}" >&2
  exit 1
fi
