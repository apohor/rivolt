#!/usr/bin/env bash
# The contract gate's own tests: a throwaway repo with final and rc tags, then migrations added on top.
set -uo pipefail
script="$(cd "$(dirname "$0")" && pwd)/contract-check.sh"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q . && git config user.email t@t && git config user.name t && git config commit.gpgsign false
mkdir -p internal/db/migrations
echo "thing: old_col" > code.txt
echo "CREATE TABLE t (id int);" > internal/db/migrations/0001_t.sql
git add -A && git commit -qm one && git tag v0.1.0
echo "x" >> code.txt && git add -A && git commit -qm two && git tag v0.2.0
echo "y" >> code.txt && git add -A && git commit -qm three && git tag v0.3.0
git tag v0.4.0-rc.1
base=$(git rev-parse HEAD)

fails=0
# run NAME WANT_EXIT 'migration body' 'PR body'
run() {
  local name="$1" want="$2" body="$3" pr="$4"
  git checkout -q "$base" 2>/dev/null; git reset -q --hard "$base"
  printf '%s\n' "$body" > internal/db/migrations/0002_x.sql
  git add -A && git commit -qm add
  PR_BODY="$pr" bash "$script" "$base" HEAD >out.txt 2>&1; got=$?
  if [ "$got" != "$want" ]; then echo "FAIL $name: exit $got, want $want"; cat out.txt; fails=$((fails + 1)); else echo "ok   $name"; fi
}
run "not a contract migration" 0 "ALTER TABLE t ADD COLUMN x int;" ""
run "an unfilled template line on a PR with no contract migration" 0 "ALTER TABLE t ADD COLUMN x int;" "Pinned now: prod vX.Y.Z, demo vX.Y.Z, admin vX.Y.Z"
run "unfilled template line with a contract migration" 1 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "Pinned now: prod vX.Y.Z, demo vX.Y.Z, admin vX.Y.Z"
run "contract without contract-after" 1 $'-- contract: drops t.old_col\nALTER TABLE t DROP COLUMN old_col;' "Pins: prod=v0.3.0"
run "older than newest, pins ok" 0 $'-- contract: drops t.old_col\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN old_col;' "Pins: prod=v0.3.0 demo=v0.2.0"
run "same as the newest final" 1 $'-- contract: x\n-- contract-after: v0.3.0\nALTER TABLE t DROP COLUMN x;' "Pins: prod=v0.3.0"
run "an rc tag" 1 $'-- contract: x\n-- contract-after: v0.4.0-rc.1\nALTER TABLE t DROP COLUMN x;' "Pins: prod=v0.3.0"
run "a tag that does not exist" 1 $'-- contract: x\n-- contract-after: v0.9.9\nALTER TABLE t DROP COLUMN x;' "Pins: prod=v0.3.0"
run "newer than the oldest pin" 1 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "Pins: prod=v0.3.0 admin=v0.1.0"
run "pinned-now line, comma form" 0 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "Pinned now: prod v0.3.0, demo v0.2.0, admin v0.2.0"
run "pinned-now line older than the tag" 1 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "Pinned now: prod v0.3.0, admin v0.1.0"
run "pinned-now with an rc" 1 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "Pinned now: prod v0.4.0-rc.1"
run "pinned-now with no tag" 1 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "Pinned now: prod latest"
run "no pins line" 1 $'-- contract: x\n-- contract-after: v0.2.0\nALTER TABLE t DROP COLUMN x;' "nothing here"
run "bad pins entry" 1 $'-- contract: x\n-- contract-after: v0.1.0\nALTER TABLE t DROP COLUMN x;' "Pins: prod=latest"
run "unreleased, never mentioned" 0 $'-- contract: x\n-- contract-after: unreleased brand_new_col added and dropped in one series\nALTER TABLE t DROP COLUMN brand_new_col;' ""
run "unreleased, but a release reads it" 1 $'-- contract: x\n-- contract-after: unreleased old_col nobody read it\nALTER TABLE t DROP COLUMN old_col;' ""
run "unreleased without a reason" 1 $'-- contract: x\n-- contract-after: unreleased brand_new_col\nALTER TABLE t DROP COLUMN brand_new_col;' ""
run "garbage contract-after" 1 $'-- contract: x\n-- contract-after: soon\nALTER TABLE t DROP COLUMN x;' "Pins: prod=v0.3.0"
# An existing migration that changes is not "added": grandfathered.
git checkout -q "$base" 2>/dev/null; git reset -q --hard "$base"
printf '%s\n' $'-- contract: legacy\nALTER TABLE t DROP COLUMN x;' > internal/db/migrations/0001_t.sql
git add -A && git commit -qm edit
if PR_BODY="" bash "$script" "$base" HEAD >out.txt 2>&1; then echo "ok   an edited existing migration is not checked"; else echo "FAIL edited existing migration"; cat out.txt; fails=$((fails + 1)); fi
[ "$fails" = 0 ] || { echo "$fails failure(s)"; exit 1; }
echo "all contract-check tests passed"
