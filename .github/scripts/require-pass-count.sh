#!/usr/bin/env bash
# require-pass-count.sh LOG COUNT TEST...   (the log of a `go test -v -count=COUNT` run)
# Fails unless each TEST's top-level "--- PASS: TEST (" line appears exactly COUNT times, and no top-level test failed or was skipped.
# "ok" alone cannot tell a skipped test from a passing one; this makes the log prove that every named test ran COUNT times and passed.
set -u
log=${1:?usage: require-pass-count.sh LOG COUNT TEST...}; want=${2:?count}; shift 2
[ -r "$log" ] || { echo "require-pass-count: cannot read $log" >&2; exit 2; }
rc=0
for t in "$@"; do
  n=$(grep -c -E "^--- PASS: ${t} \(" "$log")
  if [ "$n" != "$want" ]; then echo "require-pass-count: $t passed $n times, want $want" >&2; rc=1; else echo "require-pass-count: $t passed $n times"; fi
done
if grep -E -q "^--- (FAIL|SKIP):" "$log"; then
  echo "require-pass-count: failed or skipped tests in the log:" >&2
  grep -E "^--- (FAIL|SKIP):" "$log" | sort | uniq -c >&2
  rc=1
fi
exit $rc
