#!/usr/bin/env bash
# Tests for preview-tags-agree.sh: an admin tag on a release candidate (quoted) or anything else is brought to the preview's, and a file
# with no tag line, or two, fails instead of passing.
set -uo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
t=$(mktemp -d)   # left for the runner to discard
fail=0
case_() { # name preview-tag admin-line want-exit want-admin-tag
  printf 'image:\n  repository: x\n  tag: %s\n' "$2" >"$t/p.yaml"
  printf 'image:\n  repository: y\n%s\n' "$3" >"$t/a.yaml"
  out=$(bash "$here/preview-tags-agree.sh" "$t/p.yaml" "$t/a.yaml" 2>&1); code=$?
  got=$(grep -E '^[[:space:]]+tag:' "$t/a.yaml" | head -1 | sed -E 's/^[[:space:]]+tag:[[:space:]]+//')
  if [ "$code" = "$4" ] && { [ -z "$5" ] || [ "$got" = "$5" ]; }; then echo "ok   $1"; else echo "FAIL $1: exit $code (want $4), admin tag '$got' (want '$5'):"; echo "$out"; fail=1; fi
}
case_ "sha tag follows a sha tag" "sha-abc1234" "  tag: sha-0000000" 0 "sha-abc1234"
case_ "a leftover rc tag is replaced" "sha-abc1234" '  tag: "1.3.1-rc.1"' 0 "sha-abc1234"
case_ "a pinned preview pins the admin too" '"1.3.1-rc.1"' "  tag: sha-0000000" 0 '"1.3.1-rc.1"'
case_ "a trailing comment is not part of the tag" "sha-abc1234" "  tag: sha-0000000  # pinned" 0 "sha-abc1234"
case_ "no tag line in the admin file fails" "sha-abc1234" "  other: 1" 1 ""
case_ "two tag lines in the admin file fail" "sha-abc1234" $'  tag: sha-1\n  tag: sha-2' 1 ""
# check mode writes nothing and fails on a difference
printf '  tag: sha-aaaaaaa\n' >"$t/p.yaml"; printf '  tag: "1.3.1-rc.1"\n' >"$t/a.yaml"
if bash "$here/preview-tags-agree.sh" "$t/p.yaml" "$t/a.yaml" check >/dev/null 2>&1; then echo "FAIL check passed on a difference"; fail=1; else echo "ok   check fails on a difference"; fi
grep -q 'rc.1' "$t/a.yaml" && echo "ok   check wrote nothing" || { echo "FAIL check changed the file"; fail=1; }
printf '  tag: sha-aaaaaaa\n' >"$t/a.yaml"
bash "$here/preview-tags-agree.sh" "$t/p.yaml" "$t/a.yaml" check >/dev/null 2>&1 && echo "ok   check passes when equal" || { echo "FAIL check failed on equal tags"; fail=1; }
exit $fail
