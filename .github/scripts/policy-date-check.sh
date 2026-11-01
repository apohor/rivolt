#!/usr/bin/env bash
# policy-date-check.sh BASE HEAD: a change to a policy page's text must also change its "Last updated" date, and move it forward.
#
# DA's standing rule: when the Privacy Policy or the Terms change, the date at the top changes in the same PR (it is what tells a
# reader, and the app's "what changed" notice, which version they are on). The pages are web/src/content/privacy.md and terms.md.
# The October 23, 2026 Terms (terms-2026-10-23.md, served at /terms/2026-10-23) are frozen and are not listed: they must never change
# at all, which web/src/content/terms-frozen.test.ts enforces byte for byte. support.md and the other pages carry no date.
#
# A page that gains a "Last updated" line joins the pages list below in the same PR that adds the date, so the rule and its check never drift.
# A date that is already the day of the change (a second edit the same day) may stay.
#
# Exit 0 when every listed page that changed in BASE..HEAD also moved its date forward (or its text did not change), 1 otherwise.
set -euo pipefail

base="${1:?base rev}"
head="${2:?head rev}"
pages=(web/src/content/privacy.md web/src/content/terms.md)

# dateline REV FILE prints the "Last updated: ..." line of FILE at REV, or nothing.
dateline() { git show "$1:$2" 2>/dev/null | grep -m1 -E '^Last updated: ' || true; }

# datekey "Last updated: October 31, 2026" prints 20261031 (sortable), or nothing when it is not "Month D, YYYY". awk, not date -d,
# so it behaves the same on a laptop (BSD date) and in CI.
datekey() {
  echo "${1#Last updated: }" | awk '{
    split("January February March April May June July August September October November December", M, " ")
    for (i = 1; i <= 12; i++) if ($1 == M[i]) m = i
    d = $2; sub(/,$/, "", d)
    if (m && d ~ /^[0-9]+$/ && $3 ~ /^[0-9][0-9][0-9][0-9]$/ && d >= 1 && d <= 31) printf "%04d%02d%02d", $3, m, d
  }'
}

fail=0
for f in "${pages[@]}"; do
  git diff --quiet "$base" "$head" -- "$f" && continue                      # unchanged
  git cat-file -e "$base:$f" 2>/dev/null || continue                        # new page: nothing to bump
  old=$(dateline "$base" "$f"); new=$(dateline "$head" "$f")
  # Only the date line changed (or only whitespace): that is a date edit, not a text change.
  body=$(git diff -U0 "$base" "$head" -- "$f" | grep -E '^[+-]' | grep -v -E '^(\+\+\+|---)' | grep -v -E '^[+-]Last updated: ' | grep -E -v '^[+-][[:space:]]*$' || true)
  [ -n "$body" ] || continue
  if [ -z "$new" ]; then
    echo "::error file=$f::$f has no \"Last updated: <date>\" line"; fail=1; continue
  fi
  oe=$(datekey "$old"); ne=$(datekey "$new")
  if [ "$old" = "$new" ]; then
    # Unchanged is right only when the date already is the day of this change (a second edit the same day): the head commit's
    # date, in UTC or in Pacific time (the team's day).
    day_utc=$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y%m%d "$head")
    day_pt=$(TZ=America/Los_Angeles git log -1 --format=%cd --date=format-local:%Y%m%d "$head")
    if [ -n "$ne" ] && { [ "$ne" = "$day_utc" ] || [ "$ne" = "$day_pt" ]; }; then continue; fi
    echo "::error file=$f::$f changed but its date did not: bump \"$new\" to the day of this change (DA's rule: a policy text change moves its Last updated date)"; fail=1; continue
  fi
  if [ -z "$ne" ]; then
    echo "::error file=$f::cannot read the date in \"$new\" (write it as Month D, YYYY)"; fail=1; continue
  fi
  if [ -n "$oe" ] && [ "$ne" -lt "$oe" ]; then
    echo "::error file=$f::\"$new\" is earlier than the previous \"$old\""; fail=1; continue
  fi
done
exit $fail
