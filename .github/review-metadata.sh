#!/usr/bin/env bash
set -euo pipefail

body_file=${1:?usage: review-metadata.sh <pr-body-file> <head-sha> [changed-files-file]}
head_sha=${2:?usage: review-metadata.sh <pr-body-file> <head-sha> [changed-files-file]}
changed_files_file=${3:-}

if [[ ! -f "$body_file" ]]; then
  echo "::error::pull request body file does not exist: $body_file"
  exit 1
fi
if [[ -n "$changed_files_file" && ! -f "$changed_files_file" ]]; then
  echo "::error::changed-files file does not exist: $changed_files_file"
  exit 1
fi

body=$(<"$body_file")
changed_files=""
if [[ -n "$changed_files_file" ]]; then
  changed_files=$(<"$changed_files_file")
fi
errors=0

error() {
  echo "::error::$*"
  errors=$((errors + 1))
}

checked_count() {
  local pattern=$1
  local matches
  matches=$(printf '%s\n' "$body" | grep -Eic -- "$pattern" || true)
  printf '%s' "$matches"
}

checked() {
  local text=$1
  printf '%s\n' "$body" | grep -Fqi -- "- [x] $text" \
    || printf '%s\n' "$body" | grep -Fqi -- "- [X] $text"
}

selected() {
  local option=$1
  printf '%s\n' "$body" | grep -Fqi -- "- [x] **${option}**" \
    || printf '%s\n' "$body" | grep -Fqi -- "- [X] **${option}**"
}

field_value() {
  local label=$1
  printf '%s\n' "$body" \
    | grep -Eim1 "^\*\*${label}:\*\*" \
    | sed -E "s/^\*\*${label}:\*\*[[:space:]]*//" \
    | sed -E 's/[[:space:]]*<!--[[:space:]]*.*$//' \
    | xargs || true
}

row_value() {
  local label=$1
  printf '%s\n' "$body" \
    | grep -Fm1 "| $label |" \
    | cut -d '|' -f 4 \
    | xargs || true
}

classification_count=$(checked_count '^- \[[xX]\] \*\*(Low|Normal|High|Architectural)\*\*')
if [[ "$classification_count" -ne 1 ]]; then
  error "select exactly one review classification"
fi

route_count=$(checked_count '^- \[[xX]\] \*\*(Not required|Complete)\*\*')
if [[ "$route_count" -ne 1 ]]; then
  error "select exactly one independent-review route"
fi

classification_rationale=$(field_value 'Classification rationale')
failure_impact=$(field_value 'Failure impact')
highest_scrutiny=$(field_value 'Highest-scrutiny area')
authorities=$(field_value 'Authorities')
evidence_sha=$(field_value 'Evidence head SHA')

[[ -n "$classification_rationale" ]] || error "classification rationale is required"
[[ -n "$failure_impact" ]] || error "failure impact is required"
[[ -n "$highest_scrutiny" ]] || error "highest-scrutiny area is required"
[[ -n "$authorities" ]] || error "authority paths are required"

if [[ "$evidence_sha" != "$head_sha" ]]; then
  error "Evidence head SHA must equal the current PR head SHA ($head_sha)"
fi

if ! printf '%s\n' "$body" | grep -Eq '^\| `make ci` — review \+ lint \+ race \|[[:space:]]*0[[:space:]]*\|[[:space:]]*https://[^|[:space:]]+[[:space:]]*\|'; then
  error "record successful make ci evidence with exit status 0 and an https CI/raw-output link"
fi

requires_high=false
if [[ -n "$changed_files" ]] && printf '%s\n' "$changed_files" | grep -Eq \
  '^(\.github/review-metadata\.sh|\.github/workflows/|Makefile$|VERSION$|adr/|src/go/internal/(activation|accounts|agentbridge|anyerr|app|artifact|atomicfile|bindings|catalog|cli|client|compensation|config|db|desktop|discovery|doctor|egress|hub|journal|lock|modelcache|observe|ownership|paths|plugins|pools|presets|profile|protocol|provider|proxy|pubgate|refs|runtime|store|transfer|transport|tui|upstream)/|src/go/cmd/anypick-release/|src/macos/AppServiceClient\.swift$|src/macos/scripts/)'; then
  requires_high=true
fi

if [[ "$requires_high" == true ]] && ! selected 'High' && ! selected 'Architectural'; then
  error "changed files require High or Architectural classification"
fi

if selected 'High' || selected 'Architectural'; then
  if ! selected 'Complete'; then
    error "high and architectural changes require completed independent review"
  fi

  for label in Reviewer 'Reviewed head SHA' 'Review link' 'Evidence auditor' 'Evidence link'; do
    value=$(field_value "$label")
    if [[ -z "$value" || "$value" =~ ^[Nn]/?[Aa]$ ]]; then
      error "$label is required for high and architectural changes"
    fi
  done

  reviewed_sha=$(field_value 'Reviewed head SHA')
  if [[ "$reviewed_sha" != "$head_sha" ]]; then
    error "Reviewed head SHA must equal the current PR head SHA ($head_sha)"
  fi

  for item in \
    'Reviewer received the exact diff plus the authority paths' \
    'Findings include path, authority, failure mode, and required regression' \
    'Every P0/P1 is closed; P0/P1 was not deferred' \
    'Changed hunks were re-reviewed and affected check re-run after fixes'; do
    checked "$item" || error "required high-risk confirmation is unchecked: $item"
  done
else
  if ! selected 'Not required'; then
    error "low and normal changes must select Not required for independent review"
  fi
fi

if [[ -n "$changed_files" ]]; then
  if printf '%s\n' "$changed_files" | grep -Eq '^src/macos/'; then
    value=$(row_value 'macOS: `make test-macos`')
    [[ -n "$value" && ! "$value" =~ ^(not[[:space:]]+applicable|gate[[:space:]]+not[[:space:]]+run) ]] \
      || error "macOS changes require test-macos evidence"
  fi
  if printf '%s\n' "$changed_files" | grep -Eq '^src/tauri/'; then
    value=$(row_value 'Tauri: full CI job — typecheck, UI build, Rust check/format/test/clippy/audit')
    [[ -n "$value" && ! "$value" =~ ^(not[[:space:]]+applicable|gate[[:space:]]+not[[:space:]]+run) ]] \
      || error "Tauri changes require the full Tauri CI evidence"
  fi
  if printf '%s\n' "$changed_files" | grep -Eq '^(docs/|README\.md$)'; then
    value=$(row_value 'Docs: `make check-docs build-docs`')
    [[ -n "$value" && ! "$value" =~ ^(not[[:space:]]+applicable|gate[[:space:]]+not[[:space:]]+run) ]] \
      || error "docs changes require check-docs and build-docs evidence"
  fi
fi

if [[ "$errors" -ne 0 ]]; then
  echo "$errors review metadata error(s) found"
  exit 1
fi

echo "review metadata OK for $head_sha"
