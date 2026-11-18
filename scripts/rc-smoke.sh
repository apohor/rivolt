#!/usr/bin/env bash
# rc-smoke.sh vX.Y.Z-rc.N: the release-candidate gate. Waits until https://preview.rivolt.dev/api/health reports the rc's version, runs
# web/e2e/rc-smoke-live.pw.ts against preview (signed-out demo: Now, Activity, Money, the planner and a preset; 393 px, light and dark; no console
# errors, no failed API call), then posts the commit status qa/rc-gate on the rc's commit (success or failure, with a short description).
# Runs on a machine Cloudflare Access lets through (preview is behind it; CI cannot reach it). One browser at a time. Signs in nowhere.
#
# RC_GATE_CONTEXT=qa/something  post there instead of qa/rc-gate (a trial run).   RC_WAIT_MIN=40  minutes to wait for the version.
# A success on the context that is not this script's (description not starting "rc smoke") is never overwritten; set-qa-gate.sh still works.
# Not covered: signed-in pages (no preview account here), and iOS (its UI tests use fixtures and run in ios.yml).
set -euo pipefail
TAG=${1:?usage: scripts/rc-smoke.sh vX.Y.Z-rc.N}
[[ $TAG =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]] || { echo "not an rc tag: $TAG" >&2; exit 2; }
REPO=${RC_REPO:-apohor/rivolt}
PREVIEW=${RC_PREVIEW:-https://preview.rivolt.dev}
CONTEXT=${RC_GATE_CONTEXT:-qa/rc-gate}
WANT=${TAG#v}
WEB="$(cd "$(dirname "$0")/.." && pwd)/web"
SHA=$(gh api "repos/$REPO/commits/$TAG" --jq .sha) || { echo "rc tag $TAG not found in $REPO" >&2; exit 2; }

post() { # state description
  local cur
  cur=$(gh api "repos/$REPO/commits/$SHA/status" --jq "[.statuses[] | select(.context == \"$CONTEXT\")][0] | (.state // \"none\") + \"|\" + (.description // \"\")")
  if [ "${cur%%|*}" = success ] && [[ ${cur#*|} != "rc smoke"* ]]; then echo "a person's success is on $CONTEXT; not touching it"; return 0; fi
  gh api -X POST "repos/$REPO/statuses/$SHA" -f state="$1" -f context="$CONTEXT" -f description="$2" >/dev/null
  echo "$CONTEXT = $1 on ${SHA:0:7}: $2"
}

post pending "rc smoke: waiting for preview to run $WANT"

deadline=$((SECONDS + ${RC_WAIT_MIN:-40} * 60)); seen=0; got=none
while [ $SECONDS -lt $deadline ]; do
  got=$(curl -fsS --max-time 15 "$PREVIEW/api/health" 2>/dev/null | jq -r '.version // "none"' 2>/dev/null || echo unreachable)
  if [ "$got" = "$WANT" ]; then seen=$((seen + 1)); else seen=0; fi
  [ $seen -ge 3 ] && break   # three reads in a row: a half-rolled preview can still answer from an old pod
  sleep 20
done
if [ $seen -lt 3 ]; then post failure "rc smoke: preview reports $got, not $WANT, after ${RC_WAIT_MIN:-40} min"; exit 1; fi

cd "$WEB"
[ -d node_modules ] || npm ci --no-audit --no-fund
if E2E_LIVE_URL="$PREVIEW" CI=true npx playwright test e2e/rc-smoke-live.pw.ts --project=chromium --workers=1 --retries=1; then
  post success "rc smoke passed on preview $WANT"
else
  post failure "rc smoke failed on preview $WANT (web/e2e/rc-smoke-live.pw.ts)"; exit 1
fi
