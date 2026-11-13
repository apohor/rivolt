#!/usr/bin/env bash
# contract-gate.sh TAG: the destructive-migration gate in front of prod's pin commit (release-gitops, DA P1). Once prod is an autosync
# Application the pin commit IS the deploy, so this runs before bump-prod writes it. It lists the migration files TAG adds or changes since
# the previous final v-tag (prereleases and ios-v tags are skipped) and refuses when any carries a "-- contract:" marker. It fails closed:
# no previous final tag, or an unreadable range, stops the release. Break-glass: no bump here; BACKUP=1, no open charge and the reviewer's
# ack, then the pin is committed by hand in Git, with the migration range in the PR body. Run from a full-history checkout (fetch-depth 0).
set -euo pipefail
tag=${1:?usage: contract-gate.sh TAG}
dir=internal/db/migrations
prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' "$tag^" 2>/dev/null) || prev=""
if [ -z "$prev" ]; then
  echo "::error::no previous final v-tag before $tag: can't list its migrations, so the contract gate stops the release"
  exit 1
fi
files=$(git diff --name-only --diff-filter=ACMR "$prev" "$tag" -- "$dir") || { echo "::error::can't diff $prev..$tag"; exit 1; }
if [ -z "$files" ]; then
  echo "no migration changes in $tag (since $prev)"
  exit 0
fi
echo "migrations in $tag (since $prev):"
bad=0
while IFS= read -r f; do
  echo "  ${f##*/}"
  if git show "$tag:$f" | grep -q '^[[:space:]]*--[[:space:]]*contract:'; then
    echo "::error::${f##*/} is destructive (contract marker)"
    bad=1
  fi
done <<<"$files"
if [ "$bad" -ne 0 ]; then
  echo "::error::$tag has a contract migration: prod's pin is not bumped. Break-glass: BACKUP=1, no open charge and the reviewer's ack, then commit the pin by hand (migration range $prev..$tag in the PR body)"
  exit 1
fi
