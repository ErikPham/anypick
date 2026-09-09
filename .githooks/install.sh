#!/bin/sh
#
# Install the AnyPick commit gates: .githooks/install.sh
#
# Git ignores .git/hooks/ when committing, so hooks that live there are
# invisible to every other clone and to CI -- exactly the failure that let
# CODE_REVIEW.md sit uncommitted for a week. These hooks live in .githooks/ and
# are tracked; this script only points core.hooksPath at them.
#
# Run it once per clone. It is idempotent.

set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

if [ ! -d .githooks ]; then
  printf '%s\n' "install: no .githooks/ directory at $root" >&2
  exit 1
fi

chmod +x .githooks/commit-msg .githooks/pre-push .githooks/install.sh

git config core.hooksPath .githooks

configured=$(git rev-parse --git-path hooks)
printf '%s\n' "install: core.hooksPath -> $configured"

# A hook that is not in the repository is a hook nobody else runs. `git
# check-ignore` skips tracked paths, so ask about the pattern, not the index.
for f in .githooks/commit-msg .githooks/pre-push .githooks/install.sh; do
  if git check-ignore --no-index -q "$f"; then
    printf '%s\n' "install: WARNING $f is excluded by .gitignore -- it will never reach another clone" >&2
  fi
done

printf '%s\n' "install: done. Verify with:"
printf '%s\n' "  printf 'bad subject\n' > /tmp/m && .githooks/commit-msg /tmp/m; echo \"exit=\$?\""
