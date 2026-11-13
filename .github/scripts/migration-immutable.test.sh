#!/usr/bin/env bash
# The immutability check's own tests: a throwaway repo with applied migrations, then changes on top.
set -uo pipefail
script="$(cd "$(dirname "$0")" && pwd)/migration-immutable.sh"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q . && git config user.email t@t && git config user.name t && git config commit.gpgsign false
mkdir -p internal/db/migrations
echo "CREATE TABLE t (id int);" > internal/db/migrations/0001_t.sql
echo "ALTER TABLE t ADD COLUMN a int;" > internal/db/migrations/0002_a.sql
echo "other" > README
mkdir -p internal/db/baseline
echo "-- baseline-of: v1" > internal/db/baseline/0000_baseline.sql
git add -A && git commit -qm base
base=$(git rev-parse HEAD)

fails=0
# run NAME WANT_EXIT LABELS COMMAND...: apply COMMAND on top of base, commit, run the check.
run() {
  local name="$1" want="$2" labels="$3"; shift 3
  git reset -q --hard "$base"
  "$@"
  git add -A && git commit -qm change --allow-empty
  LABELS="$labels" bash "$script" "$base" HEAD >out.txt 2>&1; got=$?
  if [ "$got" != "$want" ]; then echo "FAIL $name: exit $got, want $want"; cat out.txt; fails=$((fails + 1)); else echo "ok   $name"; fi
}
add_new()    { echo "ALTER TABLE t ADD COLUMN b int;" > internal/db/migrations/0003_b.sql; }
edit_old()   { echo "-- harmless" >> internal/db/migrations/0001_t.sql; }
delete_old() { git rm -q internal/db/migrations/0002_a.sql; }
rename_old() { git mv internal/db/migrations/0002_a.sql internal/db/migrations/0002_renamed.sql; }
edit_and_add() { edit_old; add_new; }
edit_baseline() { echo "-- x" >> internal/db/baseline/0000_baseline.sql; }
other_file() { echo more >> README; }

run "a new migration is added" 0 "" add_new
run "nothing under migrations changes" 0 "" other_file
run "the baseline is edited" 1 "" edit_baseline
run "an applied migration is edited" 1 "" edit_old
run "an applied migration is deleted" 1 "" delete_old
run "an applied migration is renamed" 1 "" rename_old
run "an edit next to an added file" 1 "" edit_and_add
run "an edit with the amend label" 0 "bug,migration-amend" edit_old
run "an edit with a look-alike label" 1 "migration-amend-not,x" edit_old
run "an edit with other labels" 1 "bug,docs" edit_old
[ "$fails" = 0 ] || { echo "$fails failure(s)"; exit 1; }
echo "all migration-immutable tests passed"
