#!/usr/bin/env bash
# Self-test of require-pass-count.sh with fake logs. Run: bash .github/scripts/require-pass-count_test.sh
set -u
here=$(cd "$(dirname "$0")" && pwd); tool="$here/require-pass-count.sh"; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
expect() {  # expect WANT_RC NAME LOG COUNT TEST...
  local want=$1 name=$2 log=$3; shift 3
  "$tool" "$log" "$@" >/dev/null 2>&1; local rc=$?
  if [ "$rc" != "$want" ]; then echo "FAIL: $name (rc $rc, want $want)"; fail=1; else echo "ok: $name"; fi
}
gen() {  # gen FILE TEST COUNT [STATUS]
  local i; for ((i = 0; i < $3; i++)); do printf '=== RUN   %s\n    --- PASS: %s/sub (0.00s)\n--- %s: %s (0.01s)\n' "$2" "$2" "${4:-PASS}" "$2"; done >> "$1"
}
A=TestStaleRaiseRacingASignInOrOutCommitCannotFlagTheUser; B=TestAFlagRaisedBetweenLoginsClearAndTheSaveIsClearedByTheSave
: > "$tmp/good"; gen "$tmp/good" $A 20; gen "$tmp/good" $B 20; echo "ok  	github.com/apohor/rivolt/internal/api	27.510s" >> "$tmp/good"
expect 0 "both tests 20 times, all pass" "$tmp/good" 20 $A $B
: > "$tmp/few"; gen "$tmp/few" $A 20; gen "$tmp/few" $B 19
expect 1 "one test 19 times" "$tmp/few" 20 $A $B
: > "$tmp/skip"; gen "$tmp/skip" $A 20; gen "$tmp/skip" $B 20; printf -- '--- SKIP: TestSomethingElse (0.00s)\n' >> "$tmp/skip"
expect 1 "a skipped test in the log" "$tmp/skip" 20 $A $B
: > "$tmp/fail"; gen "$tmp/fail" $A 20; gen "$tmp/fail" $B 20; gen "$tmp/fail" TestOther 1 FAIL
expect 1 "a failed test in the log" "$tmp/fail" 20 $A $B
: > "$tmp/many"; gen "$tmp/many" $A 21; gen "$tmp/many" $B 20
expect 1 "21 passes is not 20" "$tmp/many" 20 $A $B
: > "$tmp/none"; echo "ok  	github.com/apohor/rivolt/internal/api	0.1s" > "$tmp/none"
expect 1 "an ok line with no test lines proves nothing" "$tmp/none" 20 $A $B
: > "$tmp/prefix"; gen "$tmp/prefix" "${A}Extra" 20; gen "$tmp/prefix" $B 20
expect 1 "a test whose name only starts with the wanted name" "$tmp/prefix" 20 $A $B
expect 2 "an unreadable log" "$tmp/nope" 20 $A
exit $fail
