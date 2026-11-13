#!/usr/bin/env bash
# migration-number.sh BASE HEAD: a migration file the PR adds sits above every number on the base branch (migrations v2).
#
# Why: two PRs that pick the same next number both pass CI and the second to merge breaks main, and a number below one that is already applied is
# a file some installs ran in another order than a fresh one. The runner sorts by name, so an out-of-order file still applies, which hides the
# mistake. 0121 is reserved: it was never used and must stay unused (an install may carry a stray row for it).
#
# Checks only the files the PR ADDS (--diff-filter=A): editing or deleting is migration-immutable's. Exit 0 when fine, 1 otherwise.
set -euo pipefail

base="${1:?base rev}"
head="${2:?head rev}"
dir="internal/db/migrations"
reserved="0121"

num() { basename "$1" | sed -n 's/^\([0-9]\{4\}\)_.*\.sql$/\1/p'; }

added=$(git diff --name-only --no-renames --diff-filter=A "$base" "$head" -- "$dir" || true)
if [ -z "$added" ]; then
  echo "migration-number: no migration added"
  exit 0
fi

max=$(git ls-tree -r --name-only "$base" -- "$dir" | while read -r f; do num "$f"; done | sort | tail -n1)
max="${max:-0000}"
fail=0
seen=""
for f in $added; do
  n=$(num "$f")
  if [ -z "$n" ]; then
    echo "migration-number: $f does not match NNNN_name.sql" >&2; fail=1; continue
  fi
  if [ "$n" = "$reserved" ]; then
    echo "migration-number: $f uses the reserved number $reserved" >&2; fail=1
  fi
  if [ "$((10#$n))" -le "$((10#$max))" ]; then
    echo "migration-number: $f is not above the base branch's highest number $max (rebase and renumber)" >&2; fail=1
  fi
  case " $seen " in *" $n "*) echo "migration-number: two added files use number $n" >&2; fail=1 ;; esac
  seen="$seen $n"
done
[ "$fail" = 0 ] && echo "migration-number: added files sit above $max"
exit "$fail"
