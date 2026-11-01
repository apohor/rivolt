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
today=$(date -u '+%B %-d, %Y' 2>/dev/null || date -u '+%B %d, %Y')
sed -i.bak "s/^Last updated: .*/Last updated: $today/" web/src/content/privacy.md && rm web/src/content/privacy.md.bak; git add -A; git commit -qm "dated today"; base=$(git rev-parse HEAD)
echo "We keep C." >> web/src/content/privacy.md;                              check "second edit on the day the date already says" 0
sed -i.bak 's/^Last updated: .*/Last updated: October 31, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; git add -A; git commit -qm "back to old"; base=$(git rev-parse HEAD)
sed -i.bak 's/^Last updated: .*/Last updated: November 2, 2026/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "date alone changed" 0
printf '\n' >> web/src/content/privacy.md;                                      check "blank line only" 0
echo "We keep B." >> web/src/content/privacy.md
sed -i.bak 's/^Last updated: .*/Last updated: soon/' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "text changed, date unreadable" 1
echo "We keep B." >> web/src/content/privacy.md
sed -i.bak '/^Last updated: /d' web/src/content/privacy.md && rm web/src/content/privacy.md.bak; check "text changed, date line removed" 1
exit $fails
