#!/usr/bin/env bash
# final-gate.sh TAG SHA: the release-candidate gate on a final tag (release-gitops PR 7b), run by tag-validate for every vX.Y.Z push, so a
# hand-pushed final meets the same rule as ship-release.sh and release.yml. The final's commit must be a release candidate's commit
# (vX.Y.Z-rc.N at SHA), that rc's qa/rc-gate status must be success, and preview must be running its image (preview and admin-preview values).
# Without an rc at SHA the only way through is a logged bypass: rivolt-infra's ops/release/rc-bypass.log holds a line for this tag at this
# SHA (the release-bypass environment approval writes it before the tag; ship-release.sh NO_RC writes it too). It fails closed: anything it
# cannot read refuses the tag.
# Env: GH_REPO (this repo), INFRA_REPO (rivolt-infra), GH_TOKEN (read this repo), INFRA_TOKEN (read rivolt-infra), RC_TAG_REFS (test only: the
# `git ls-remote --tags` lines to use instead of asking origin).
set -uo pipefail
tag=${1:?usage: final-gate.sh TAG SHA}; sha=${2:?usage: final-gate.sh TAG SHA}
repo=${GH_REPO:?GH_REPO}; infra=${INFRA_REPO:?INFRA_REPO}
fail() { echo "::error::$tag: $1"; exit 1; }
infra_file() { GH_TOKEN=${INFRA_TOKEN:-} gh api -H 'Accept: application/vnd.github.raw' "repos/$infra/contents/$1"; }

# rc tags of this version and the commit each points at: an annotated tag lists twice, the object and its peeled commit (^{}), which wins.
refs=${RC_TAG_REFS-$(git ls-remote --tags origin "refs/tags/$tag-rc.*")} || fail "could not list the release candidate tags"
rcs=$(awk '{ r=$2; sub("^refs/tags/", "", r); peeled = (r ~ /\^\{\}$/); sub(/\^\{\}$/, "", r); if (peeled || !(r in c)) c[r]=$1 } END { for (r in c) print r, c[r] }' <<<"$refs")
match=$(awk -v s="$sha" '$2 == s { print $1 }' <<<"$rcs" | grep -E "^${tag//./\\.}-rc\.[1-9][0-9]*$" | sort -t. -k4 -n | tail -1 || true)

if [ -z "$match" ]; then
  log=$(infra_file ops/release/rc-bypass.log) || fail "no release candidate at ${sha:0:7} and rc-bypass.log could not be read: a final needs the gated rc's commit, or a logged bypass"
  if grep -qF " | $tag | $sha | " <<<"$log"; then
    echo "OK: no rc at ${sha:0:7}, but rc-bypass.log records the bypass for $tag at this commit (qa still owes the preview gate)."
    exit 0
  fi
  fail "$tag points at ${sha:0:7}, which is no release candidate's commit, and rc-bypass.log has no line for it. Gate an rc first (release.yml rc=N, qa/rc-gate), or log the bypass with the release-bypass environment."
fi

st=$(gh api "repos/$repo/commits/$sha/status" --jq '[.statuses[] | select(.context == "qa/rc-gate")][0].state // "none"') || fail "could not read qa/rc-gate on ${sha:0:7}"
[ "$st" = success ] || fail "qa/rc-gate on $match (${sha:0:7}) is '$st', not success: qa has not gated this release candidate"

want=${match#v}
for f in rivolt-preview rivolt-admin-preview; do
  v=$(infra_file "apps/platform/$f/values.yaml") || fail "could not read $f's values from $infra"
  got=$(grep -E '^[[:space:]]+tag:[[:space:]]' <<<"$v" | head -1 | sed -E 's/^[[:space:]]+tag:[[:space:]]+//; s/[[:space:]]*#.*$//; s/"//g')
  [ "$got" = "$want" ] || fail "$f runs image '${got:-?}', not the gated $match ($want): preview must run the rc qa gated"
done
echo "OK: $tag is $match at ${sha:0:7}, qa/rc-gate is success, preview and admin-preview run $want."
