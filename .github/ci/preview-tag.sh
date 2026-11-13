#!/usr/bin/env bash
# preview-tag.sh pin|unpin NEWTAG SOURCE_SHA MESSAGE: in the current rivolt-infra checkout (full history, remote origin, a token that can push main),
# set image.tag in BOTH preview values files (rivolt-preview, and rivolt-admin-preview which always moves with it) and push the commit to main.
#   pin   NEWTAG = X.Y.Z-rc.N (an rc's image tag): written quoted, as preview-pin.sh does; bump-preview rewrites only `tag: sha-...`, so main
#         merges leave preview alone while it is pinned.
#   unpin NEWTAG = sha-<hex> (the final's commit): written bare, the shape bump-preview rewrites, so its next build takes over. Does nothing when
#         preview is not pinned.
# Argo syncs preview by itself (rivolt-preview is a plain Application since #533). Then it re-reads origin/main and fails unless OUR commit is on
# it (and, for a pin, both files still hold the rc): a pushed commit that did not land must fail the job, never report a pin that is not there.
# Idempotent: a repeat finds the files already changed and only verifies. build.yml's pin-preview / unpin-preview jobs call it; preview-tag_test.go
# pins it against a local bare repo.
set -euo pipefail
mode=${1:?pin|unpin}; new=${2:?new tag}; src=${3:?source sha}; msg=${4:?message}
files=(apps/platform/rivolt-preview/values.yaml apps/platform/rivolt-admin-preview/values.yaml)
line='^([[:space:]]+tag:[[:space:]]+)(sha-[0-9a-f]+|"[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+")([[:space:]]*(#.*)?)$'
case "$mode" in
  pin)   [[ $new =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$ ]] || { echo "pin needs an rc image tag X.Y.Z-rc.N, got '$new'"; exit 2; }; val="\"$new\"" ;;
  unpin) [[ $new =~ ^sha-[0-9a-f]{7,40}$ ]] || { echo "unpin needs sha-<hex>, got '$new'"; exit 2; }; val=$new ;;
  *) echo "usage: preview-tag.sh pin|unpin NEWTAG SOURCE_SHA MESSAGE"; exit 2 ;;
esac
[[ $src =~ ^[0-9a-f]{40}$ ]] || { echo "source sha must be 40 hex digits"; exit 2; }

tag_of() {  # tag_of FILE: the image tag, quotes stripped; exactly one tag line or fail
  local n
  n=$(grep -cE "$line" "$1" || true)
  [ "$n" = 1 ] || { echo "$1: expected exactly one image tag line, found $n" >&2; return 1; }
  grep -E "$line" "$1" | sed -E "s@$line@\2@" | tr -d '"'
}

git config user.name >/dev/null || git config user.name "rivolt-ci"
git config user.email >/dev/null || git config user.email "ci@rivolt.dev"
mine=""
for attempt in 1 2 3 4 5; do
  git fetch -q origin main && git reset -q --hard origin/main
  if [ "$mode" = unpin ]; then
    cur=$(tag_of "${files[0]}")
    case "$cur" in sha-*) echo "preview is not pinned (tag $cur): nothing to do"; exit 0 ;; esac
  fi
  for f in "${files[@]}"; do
    tag_of "$f" >/dev/null
    sed -i.bak -E "s@$line@\1${val}\3@" "$f" && rm -f "$f.bak"
  done
  if git diff --quiet; then echo "preview already has $new"; mine=""; break; fi
  git add "${files[@]}"
  git commit -q -m "$msg" -m "Source-SHA: $src"
  mine=$(git rev-parse HEAD)
  if git push -q origin HEAD:main; then break; fi
  echo "push rejected (another commit landed); retrying ($attempt)"; mine=""; sleep $((attempt * 3))
done
git fetch -q origin main
if [ -n "$mine" ]; then
  git merge-base --is-ancestor "$mine" origin/main || { echo "::error::my $mode commit ${mine:0:7} is not on origin/main"; exit 1; }
fi
for f in "${files[@]}"; do
  got=$(git show "origin/main:$f" | { cat > "$f.chk"; tag_of "$f.chk"; rm -f "$f.chk"; })
  case "$mode" in
    pin)   [ "$got" = "$new" ] || { echo "::error::origin/main has $f at '$got', not '$new'"; exit 1; } ;;
    unpin) case "$got" in sha-*) ;; *) echo "::error::origin/main still has $f pinned at '$got'"; exit 1 ;; esac ;;
  esac
done
echo "$mode done: preview and admin-preview at $new (source ${src:0:7})"
