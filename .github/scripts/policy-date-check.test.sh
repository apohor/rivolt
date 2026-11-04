#!/usr/bin/env bash
# Tests for policy-date-check.sh, in a throwaway repo. Run: bash .github/scripts/policy-date-check.test.sh
set -uo pipefail
script="$(cd "$(dirname "$0")" && pwd)/policy-date-check.sh"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q . && git config user.email t@example.com && git config user.name t && git config commit.gpgsign false
mkdir -p web/src/content
printf '# Rivolt Privacy Policy\n\nLast updated: October 31, 2026\n\nWe keep A.\n' > web/src/content/privacy.md
printf '# Rivolt Terms of Service\n\nLast updated: October 23, 2026\n\nTerm A.\n' > web/src/content/terms.md
printf '# Rivolt Terms of Service\n\nLast updated: October 23, 2026\n\nTerm A.\n' > web/src/content/terms-2026-10-23.md
printf '# Support\n\nMail us.\n' > web/src/content/support.md
git add -A && git commit -qm base && base=$(git rev-parse HEAD)

fails=0
check() { # name expected-exit
  git add -A && git commit -qm "$1" --allow-empty
  "$script" "$base" HEAD >/dev/null 2>&1; got=$?
  if [ "$got" -ne "$2" ]; then echo "FAIL: $1 (exit $got, want $2)"; fails=1; else echo "ok:   $1"; fi
  git reset -q --hard "$base"
}

check "no policy file changed" 0
echo "More." >> web/src/content/support.md;                                    check "support.md text changed (it has no date)" 0
echo "Frozen edit." >> web/src/content/terms-2026-10-23.md;                    check "the frozen Terms are not this check's business" 0
echo "We keep B." >> web/src/content/privacy.md;                               check "privacy text changed, date not" 1
sed -i.bak 's/^Term A\./Term B./' web/src/content/terms.md && rm web/src/content/terms.md.bak; check "terms text changed, date not" 1
echo "We keep B." >> web/src/content/privacy.md
sed -i.bak 's/^Last updated: .*/Last updated: November 2, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "privacy text and date both changed forward" 0
echo "We keep B." >> web/src/content/privacy.md
sed -i.bak 's/^Last updated: .*/Last updated: October 1, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "privacy date moved backwards" 1
# The "day of the change" is the head commit's date, so these cases pin it: the commits are made at a fixed instant (GIT_COMMITTER_DATE) and the
# page says that day, so the result never depends on the calendar the test runs on. 9 is a single-digit day on purpose: the 4th to the 9th were once
# unreadable (awk compared the day as text, "9" <= "31" is false). 12:00 UTC is the 9th in Pacific time too.
export GIT_COMMITTER_DATE="2026-11-09T12:00:00Z"
sed -i.bak "s/^Last updated: .*/Last updated: November 9, 2026/" web/src/content/privacy.md && rm web/src/content/privacy.md.bak; git add -A; git commit -qm "dated that day"; base=$(git rev-parse HEAD)
echo "We keep C." >> web/src/content/privacy.md;                              check "second edit on the day the date already says" 0
export GIT_COMMITTER_DATE="2026-11-10T12:00:00Z"
echo "We keep D." >> web/src/content/privacy.md;                              check "an edit the next day with the old date" 1
unset GIT_COMMITTER_DATE
sed -i.bak 's/^Last updated: .*/Last updated: October 31, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; git add -A; git commit -qm "back to Oct 31"; base=$(git rev-parse HEAD)
for day in 4 5 6 7 8 9; do # every single-digit day reads as a date, and moves forward from the 3rd
  echo "We keep E." >> web/src/content/privacy.md
  sed -i.bak "s/^Last updated: .*/Last updated: November $day, 2026/" web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "privacy text and a date on November $day" 0
done
sed -i.bak 's/^Last updated: .*/Last updated: October 31, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; git add -A; git commit -qm "back to old"; base=$(git rev-parse HEAD)
sed -i.bak 's/^Last updated: .*/Last updated: November 2, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "date alone changed" 0
printf '\n' >> web/src/content/privacy.md;                                      check "blank line only" 0
echo "We keep B." >> web/src/content/privacy.md
sed -i.bak 's/^Last updated: .*/Last updated: soon/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "text changed, date unreadable" 1
echo "We keep B." >> web/src/content/privacy.md
sed -i.bak '/^Last updated: /d' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "text changed, date line removed" 1
exit $fails
