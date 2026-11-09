#!/bin/sh
# Freeze (or refresh) an old-client compatibility baseline from a git tag (zero-impact deploys, G5; see internal/api/compat_baseline_test.go).
#   ios/freeze-compat.sh ios-v0.2.63 ios    the iPhone build's response fixtures, request fixtures and the routes it calls
#   ios/freeze-compat.sh v0.41.4 web        the web release's route literals
# Writes ios/RivoltTests/FixturesBaseline/<tag>/ and one line in ios/compat-baseline.txt. Commit the result; dropping a baseline is a deliberate,
# reviewed deletion (it moves with min_ios_build).
set -eu
[ $# -eq 2 ] || { echo "usage: $0 <git-tag> <ios|web>" >&2; exit 2; }
cd "$(dirname "$0")/.."
git rev-parse --verify --quiet "$1^{commit}" >/dev/null || { echo "no such tag: $1" >&2; exit 2; }
RIVOLT_FREEZE_COMPAT_TAG="$1" RIVOLT_FREEZE_COMPAT_KIND="$2" go test ./internal/api -run '^TestFreezeCompatBaseline$' -count=1 -v | grep -E "froze|FAIL|ok" || true
