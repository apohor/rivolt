#!/usr/bin/env bash
# bypass-log.sh INFRA_DIR: append the NO_RC bypass line (UTC time | tag | sha | reason, from releasectl.py bypass-line) to rivolt-infra's
# ops/release/rc-bypass.log and push it to main BEFORE the final tag is cut (release.yml, bypass job; ship-release.sh's rc_bypass_log writes
# the same file the same way). INFRA_DIR is a rivolt-infra checkout with a token that can push. Fails (so the tag job never starts) when the
# line cannot be pushed. Env: TAG, SHA, REASON.
set -euo pipefail
dir=${1:?usage: bypass-log.sh INFRA_DIR}
tag=${TAG:?TAG}; sha=${SHA:?SHA}; reason=${REASON:?REASON}
log=ops/release/rc-bypass.log
git -C "$dir" config user.name "rivolt-release"; git -C "$dir" config user.email "ci@rivolt.dev"
for i in 1 2 3 4 5; do
  git -C "$dir" fetch -q origin main && git -C "$dir" reset -q --hard origin/main
  if [ -f "$dir/ops/release/releasectl.py" ]; then ctl="$dir/ops/release/releasectl.py"; else echo "::error::releasectl.py is missing from $dir"; exit 1; fi
  python3 -I "$ctl" no-rc-check "$reason" || exit 1
  mkdir -p "$dir/ops/release"
  python3 -I "$ctl" bypass-line "$tag" "$sha" "$reason" >> "$dir/$log"
  git -C "$dir" add "$log"
  git -C "$dir" commit -q --no-gpg-sign -m "release: NO_RC bypass for $tag"
  if git -C "$dir" push -q origin HEAD:main; then echo "logged the bypass for $tag at ${sha:0:7} in rivolt-infra $log"; exit 0; fi
  echo "push rejected (another commit landed); retrying ($i)"; sleep $((i * ${BYPASS_PUSH_SLEEP:-3}))
done
echo "::error::could not push the rc-bypass.log line for $tag: the final is not tagged"; exit 1
