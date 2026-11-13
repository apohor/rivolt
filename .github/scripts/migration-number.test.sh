#!/usr/bin/env bash
# migration-number.sh's own tests: a throwaway repo with migrations up to 0003.
set -uo pipefail
script="$(cd "$(dirname "$0")" && pwd)/migration-number.sh"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q . && git config user.email t@t && git config user.name t && git config commit.gpgsign false
mkdir -p internal/db/migrations
for n in 0001 0002 0003; do echo "SELECT 1;" > "internal/db/migrations/${n}_m.sql"; done
echo other > README
git add -A && git commit -qm base
base=$(git rev-parse HEAD)

fails=0
run() {
  local name="$1" want="$2"; shift 2
  git reset -q --hard "$base"
  "$@"
  git add -A && git commit -qm change --allow-empty
  bash "$script" "$base" HEAD >out.txt 2>&1; got=$?
  if [ "$got" != "$want" ]; then echo "FAIL $name: exit $got, want $want"; cat out.txt; fails=$((fails + 1)); else echo "ok   $name"; fi
}
add() { for n in "$@"; do echo "SELECT 1;" > "internal/db/migrations/${n}_new.sql"; done; }
run "nothing added" 0 true
run "the next number" 0 add 0004
run "a gap above" 0 add 0010
run "two added in order" 0 add 0004 0005
run "the same number as one on base" 1 add 0003
run "below the highest" 1 add 0002
run "two added with one number" 1 bash -c 'echo a > internal/db/migrations/0004_a.sql; echo b > internal/db/migrations/0004_b.sql'
run "a bad name" 1 bash -c 'echo a > internal/db/migrations/4_a.sql'
run "the reserved 0121" 1 add 0121
run "a README edit" 0 bash -c 'echo more >> README'
[ "$fails" = 0 ] || { echo "$fails failed"; exit 1; }
echo "all migration-number tests passed"
