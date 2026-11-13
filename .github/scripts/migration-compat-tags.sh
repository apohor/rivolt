#!/usr/bin/env bash
# migration-compat-tags.sh BASE HEAD: which old releases the old-binary-on-new-schema suite runs (migrations v2, step 3).
#
# Prints, one per line, the tags to test and sets nothing else:
#   - the newest final v* tag reachable from BASE (the rollback and canary target), and
#   - the oldest tag on the PR body's "Pinned now:" / "Pins:" line when it is a final tag older than that (ingest, admin and demo run their
#     own tags, so the schema must also serve the oldest pinned image). Absent or unfilled means only the newest is tested.
#   - every pinned tag that is not reachable from BASE (a hotfix tag cut on a release branch).
# Pins are checked against all final tags, merged or not.
# Prints nothing when the PR adds no migration file (the suite is not needed). Exit 1 for a pin that is not an existing final tag.
#
# Env: PR_BODY.
set -euo pipefail
base="${1:?base rev}"
head="${2:?head rev}"
dir="internal/db/migrations"

if [ -z "$(git diff --name-only --diff-filter=A "$base" "$head" -- "$dir" | grep -E '\.sql$' || true)" ]; then
  exit 0
fi
allfinals=$(git tag -l 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true)
finals=$(git tag -l 'v[0-9]*' --merged "$base" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true)
newest=$(printf '%s\n' "$finals" | tail -1)
[ -n "$newest" ] || { echo "migration-compat: no final release tag to test against" >&2; exit 1; }
echo "$newest"

pins=$(printf '%s\n' "${PR_BODY:-}" | grep -m1 -E '^(Pinned now|Pins):[[:space:]]' | sed -E 's/^(Pinned now|Pins):[[:space:]]*//' || true)
oldest=""
unmerged=""
for t in $(printf '%s\n' "$pins" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' || true); do
  # An unfilled template line ("vX.Y.Z") names no tag and is ignored; a real-looking tag that does not exist is an error.
  printf '%s\n' "$allfinals" | grep -qxF "$t" || { echo "migration-compat: pinned tag $t is not an existing final release tag" >&2; exit 1; }
  # A hotfix tag lives on release/X.Y and is never merged into the base: it is a real rollback target, so it is always tested.
  printf '%s\n' "$finals" | grep -qxF "$t" || unmerged="$unmerged $t"
  if [ -z "$oldest" ] || [ "$(printf '%s\n%s\n' "$t" "$oldest" | sort -V | head -1)" = "$t" ]; then oldest="$t"; fi
done
if [ -n "$oldest" ] && [ "$oldest" != "$newest" ]; then echo "$oldest"; fi
for t in $unmerged; do [ "$t" = "$oldest" ] || echo "$t"; done
