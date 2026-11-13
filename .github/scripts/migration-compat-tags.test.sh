#!/usr/bin/env bash
# The tag picker's own tests: a throwaway repo with final and rc tags.
set -uo pipefail
script="$(cd "$(dirname "$0")" && pwd)/migration-compat-tags.sh"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q . && git config user.email t@t && git config user.name t && git config commit.gpgsign false
mkdir -p internal/db/migrations
echo "CREATE TABLE t (id int);" > internal/db/migrations/0001_t.sql
git add -A && git commit -qm one && git tag v0.1.0
echo x > f && git add -A && git commit -qm two && git tag v0.2.0
echo y > f && git add -A && git commit -qm three && git tag v0.3.0 && git tag v0.4.0-rc.1
base=$(git rev-parse HEAD)
# A final tag cut on a branch that is not an ancestor of base (a hotfix line) is not a rollback target for this PR.
git checkout -q -b side && echo s > s && git add -A && git commit -qm side && git tag v0.9.0 && git checkout -q -
fails=0
# run NAME WANT_EXIT WANT_OUTPUT(comma joined) FILE PR_BODY
run() {
  local name="$1" want="$2" out="$3" file="$4" pr="$5"
  git reset -q --hard "$base"
  if [ -n "$file" ]; then echo "ALTER TABLE t ADD COLUMN x int;" > "internal/db/migrations/$file"; else echo z > other; fi
  git add -A && git commit -qm change
  PR_BODY="$pr" bash "$script" "$base" HEAD >o.txt 2>err.txt; code=$?
  got=$(paste -sd, - < o.txt)
  if [ "$got" != "$out" ] || [ "$code" != "$want" ]; then echo "FAIL $name: exit $code output '$got', want $want '$out'"; cat err.txt; fails=$((fails + 1)); else echo "ok   $name"; fi
}
run "no migration added" 0 "" "" "Pins: prod v0.1.0"
run "migration added, no pins" 0 "v0.3.0" 0002_x.sql ""
run "unfilled template line" 0 "v0.3.0" 0002_x.sql "Pinned now: prod vX.Y.Z, demo vX.Y.Z, admin vX.Y.Z"
run "oldest pin is added" 0 "v0.3.0,v0.1.0" 0002_x.sql "Pinned now: prod v0.3.0, demo v0.2.0, admin v0.1.0"
run "all pins are the newest" 0 "v0.3.0" 0002_x.sql "Pins: prod=v0.3.0 admin=v0.3.0"
run "tag outside base ancestry is ignored" 0 "v0.3.0" 0002_x.sql ""
run "pin that does not exist" 1 "v0.3.0" 0002_x.sql "Pins: prod v0.9.9"
[ "$fails" = 0 ] || { echo "$fails failure(s)"; exit 1; }
echo "all migration-compat-tags tests passed"
