#!/usr/bin/env bash
# contract-check.sh BASE HEAD: the two-release rule for contract migrations (zero-impact deploys, G6c).
#
# A rollback is the previous image on the newer schema, and a deploy leaves older images running (ingest, demo and admin pin their own tags),
# so a migration that REMOVES something (a "-- contract:" migration) may ship only after every image still running has stopped reading it.
# Every migration ADDED in BASE..HEAD that has a "-- contract:" line must also say when the readers went:
#
#   -- contract-after: vX.Y.Z        the final release in which no code reads what this removes
#   -- contract-after: unreleased <object> <why>
#                                    nothing in any release ever read <object> (a column added and dropped inside one unreleased PR series)
#
# and the PR body must carry the pins ("Pins: prod=v0.41.5 demo=v0.41.4 admin=v0.41.5": the tags the deployments run now; CI cannot read the
# infra repo). Rules, each a failure when broken:
#   - vX.Y.Z is an existing FINAL tag (no -rc), strictly older than the newest final tag L: at least one release has shipped since, so the
#     image a rollback goes to no longer reads it;
#   - vX.Y.Z is not newer than the OLDEST pin: nothing still running reads it;
#   - "unreleased": <object> is not mentioned anywhere in the tree at L (code or migrations), so "unreleased" cannot hide something L reads.
# It trusts the author's tag; the devils-advocate's ack checks the claim. Migrations that exist at BASE are grandfathered: only added files count.
#
# Env: PR_BODY (the pull request body), for the Pins line. Exit 0 when every added contract migration passes, 1 otherwise.
set -euo pipefail

base="${1:?base rev}"
head="${2:?head rev}"
body="${PR_BODY:-}"
dir="internal/db/migrations"

finals=$(git tag -l 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true)
newest=$(printf '%s\n' "$finals" | tail -1)

# older A B: true when A sorts strictly before B by version.
older() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$1" ]; }

pins=$(printf '%s\n' "$body" | grep -m1 -E '^Pins:[[:space:]]' | sed -E 's/^Pins:[[:space:]]*//' || true)
oldest_pin=""
if [ -n "$pins" ]; then
  for tok in $pins; do
    tag="${tok#*=}"
    if ! printf '%s\n' "$tag" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
      echo "contract-check: Pins entry '$tok' is not name=vX.Y.Z" >&2
      exit 1
    fi
    if [ -z "$oldest_pin" ] || older "$tag" "$oldest_pin"; then oldest_pin="$tag"; fi
  done
fi

fail=0
checked=0
while IFS= read -r f; do
  [ -n "$f" ] || continue
  content=$(git show "$head:$f")
  printf '%s\n' "$content" | grep -qE '^[[:space:]]*--[[:space:]]*contract:[[:space:]]*[^[:space:]]' || continue
  checked=$((checked + 1))
  line=$(printf '%s\n' "$content" | grep -m1 -E '^[[:space:]]*--[[:space:]]*contract-after:' | sed -E 's/^[[:space:]]*--[[:space:]]*contract-after:[[:space:]]*//' || true)
  if [ -z "$line" ]; then
    echo "contract-check: $f has a '-- contract:' line but no '-- contract-after: vX.Y.Z' (the final release in which no code reads what it removes)" >&2
    fail=1; continue
  fi
  case "$line" in
    unreleased*)
      obj=$(printf '%s\n' "$line" | awk '{print $2}')
      why=$(printf '%s\n' "$line" | cut -d' ' -f3-)
      if ! printf '%s\n' "$obj" | grep -qE '^[A-Za-z_][A-Za-z0-9_.]*$' || [ -z "$why" ]; then
        echo "contract-check: $f: 'contract-after: unreleased' needs '<object> <why>' (the identifier removed, and why no release read it)" >&2
        fail=1; continue
      fi
      if [ -n "$newest" ] && git grep -qw -e "$obj" "$newest" -- . 2>/dev/null; then
        echo "contract-check: $f says '$obj' was never released, but it is mentioned in the tree at $newest (code or migrations): a release reads it, name that release in contract-after" >&2
        fail=1; continue
      fi
      echo "contract-check: $f: unreleased '$obj' ($why): not found at ${newest:-any tag}; needs the devils-advocate's ack"
      ;;
    v*)
      t=$(printf '%s\n' "$line" | awk '{print $1}')
      if ! printf '%s\n' "$finals" | grep -qxF "$t"; then
        echo "contract-check: $f: contract-after '$t' is not an existing final release tag (vX.Y.Z, no -rc)" >&2
        fail=1; continue
      fi
      if ! older "$t" "$newest"; then
        echo "contract-check: $f: contract-after $t is not older than the newest final release $newest: the image a rollback goes to may still read it. Ship the removal of the readers first, then this one a release later." >&2
        fail=1; continue
      fi
      if [ -z "$oldest_pin" ]; then
        echo "contract-check: $f: add a 'Pins: prod=vX.Y.Z demo=vX.Y.Z admin=vX.Y.Z ...' line to the PR body (the tags the deployments run now)" >&2
        fail=1; continue
      fi
      if older "$oldest_pin" "$t"; then
        echo "contract-check: $f: contract-after $t is newer than the oldest pinned deployment $oldest_pin: that image may still read it" >&2
        fail=1; continue
      fi
      echo "contract-check: $f: readers gone in $t; newest final $newest, oldest pin $oldest_pin: ok"
      ;;
    *)
      echo "contract-check: $f: contract-after '$line' is neither vX.Y.Z nor 'unreleased <object> <why>'" >&2
      fail=1
      ;;
  esac
done < <(git diff --name-only --diff-filter=A "$base" "$head" -- "$dir" | grep -E '\.sql$' || true)

echo "contract-check: $checked contract migration(s) added; newest final release ${newest:-none}; pins: ${pins:-none given}"
exit "$fail"
