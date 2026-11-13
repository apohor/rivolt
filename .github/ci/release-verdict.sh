#!/usr/bin/env bash
# release-verdict.sh: what release.yml would do for one dispatch, without tagging anything (report-only, release-gitops PR 2). It repeats the
# checks ship-release.sh / ship-rc.sh make before they push a tag, from the runner: tag shape, tag not yet taken, a green main build at the
# SHA, no contract (destructive) migration in a final, and for a final the gated rc (same commit, qa/rc-gate success, preview values on the
# rc image) or a real no_rc_reason. Not checked here: the running preview images and the open-charge/Rivian after-checks (they need the
# cluster; the scripts and, later, the Rollouts analysis do them). Always exits 0 unless it cannot run; the verdict is in the output and
# in the step summary and in the step outputs tag, sha, refused (release.yml's tag job reads them).
# Inputs (env): VERSION (vX.Y.Z), RC (rc number to cut, empty = final), GATED_RC (final: the rc number qa gated), SHA (empty = origin/main),
# NO_RC_REASON, GH_REPO, INFRA_DIR (checkout of rivolt-infra with ops/release/releasectl.py and the two preview values files), GH_TOKEN.
set -uo pipefail
VERSION=${VERSION:-}; RC=${RC:-}; GATED_RC=${GATED_RC:-}; SHA=${SHA:-}; NO_RC_REASON=${NO_RC_REASON:-}
REPO=${GH_REPO:?GH_REPO}; INFRA_DIR=${INFRA_DIR:?INFRA_DIR}
CTL="$INFRA_DIR/ops/release/releasectl.py"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
problems=(); notes=()
refuse() { problems+=("$1"); }
note() { notes+=("$1"); }

finish() {
  local out
  if [ ${#problems[@]} -eq 0 ]; then out="WOULD TAG ${TAG:-?} at ${FULL:0:7}"; else out="WOULD REFUSE ${TAG:-$VERSION}"; fi
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    # What release.yml's tag job acts on: the verdict itself never tags.
    { echo "tag=${TAG:-}"; echo "sha=${FULL:-}"; [ ${#problems[@]} -eq 0 ] && echo "refused=false" || echo "refused=true"; } >> "$GITHUB_OUTPUT"
  fi
  {
    echo "### release verdict (nothing is tagged here)"
    echo "**$out**"
    for p in ${problems[@]+"${problems[@]}"}; do echo "- refuse: $p"; done
    for n in ${notes[@]+"${notes[@]}"}; do echo "- note: $n"; done
  } | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
  exit 0
}

if [ "$(bash "$here/tag-kind.sh" "$VERSION" 2>/dev/null)" != final ]; then refuse "version '$VERSION' is not vX.Y.Z"; finish; fi
if [ -n "$RC" ]; then
  [[ $RC =~ ^[1-9][0-9]*$ ]] || { refuse "rc '$RC' is not a positive number"; finish; }
  TAG="$VERSION-rc.$RC"
else TAG=$VERSION; fi
# The workflow cuts release candidates only. A final also needs prod's sync, which is still ship-release.sh until prod is an autosync app.
if [ -z "$RC" ] && [ "${FINAL_BY_WORKFLOW:-}" != true ]; then refuse "a final tag is not cut by this workflow yet (prod is not an autosync app): use ship-release.sh $VERSION"; fi
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then refuse "tag $TAG already exists (v* tags are immutable)"; fi

FULL=$(git rev-parse -q --verify "${SHA:-origin/main}^{commit}") || { refuse "sha '${SHA:-origin/main}' is not a commit"; finish; }
note "target $TAG at ${FULL:0:7}"

# Same rule as build.yml's tag-validate: a successful main (push or manual) or release/** push build at this exact SHA.
count=$(gh run list --repo "$REPO" --workflow build.yml --commit "$FULL" --status success --limit 20 --json headBranch,event \
  --jq '[.[] | select((.event == "push" and (.headBranch == "main" or (.headBranch | startswith("release/")))) or (.event == "workflow_dispatch" and .headBranch == "main"))] | length') \
  || { refuse "could not list the builds of ${FULL:0:7}"; count=0; }
[ "${count:-0}" -ge 1 ] || refuse "${FULL:0:7} has no successful main or release/** build"

# Migrations this release adds since the previous final (lib.sh destructive_precheck): a contract marker is break-glass.
prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' "$FULL^" 2>/dev/null)
if [ -z "$prev" ]; then refuse "no previous final tag before ${FULL:0:7}: cannot list its migrations"
else
  newm=$(git diff --name-only --diff-filter=A "$prev" "$FULL" -- internal/db/migrations | sed 's|.*/||')
  note "migrations since $prev: ${newm:-none}"
  for f in $newm; do
    if git show "$FULL:internal/db/migrations/$f" | grep -q '^[[:space:]]*--[[:space:]]*contract:'; then
      if [ -z "$RC" ]; then refuse "$f has a contract marker: a destructive migration is not an automated release (break-glass, BACKUP=1, quiet DB)"
      else note "$f has a contract marker: its final is break-glass"; fi
    fi
  done
fi

# A final promotes the rc qa gated, at the same commit (releasectl rc_gate_problem), or is a bypass with a real reason.
if [ -z "$RC" ]; then
  if [ -z "$GATED_RC" ]; then
    if [ -z "$NO_RC_REASON" ]; then refuse "name the gated rc (gated_rc) or give a no_rc_reason"
    elif ! why=$(python3 -I "$CTL" no-rc-check "$NO_RC_REASON" 2>&1); then refuse "$why"
    else note "no gated rc, reason accepted: '$NO_RC_REASON'. The real run needs the release-bypass environment approval and the rc-bypass.log line first"; fi
  else
    [[ $GATED_RC =~ ^[1-9][0-9]*$ ]] || { refuse "gated_rc '$GATED_RC' is not a positive number"; finish; }
    RCTAG="$VERSION-rc.$GATED_RC"
    rcsha=$(git rev-parse -q --verify "refs/tags/$RCTAG^{commit}" 2>/dev/null) || rcsha=""
    t1=$(python3 -I "$CTL" get-tag "$INFRA_DIR/apps/platform/rivolt-preview/values.yaml" 2>/dev/null) || t1=""
    t2=$(python3 -I "$CTL" get-tag "$INFRA_DIR/apps/platform/rivolt-admin-preview/values.yaml" 2>/dev/null) || t2=""
    if ! why=$(python3 -I "$CTL" rc-gate-check "$VERSION" "$RCTAG" "$rcsha" "$FULL" ${t1:+"$t1"} ${t2:+"$t2"} 2>&1); then refuse "$why"
    else note "rc gate: $VERSION = $RCTAG at ${FULL:0:7}; preview values are on the rc image"; fi
    if [ -n "$rcsha" ]; then
      st=$(gh api "repos/$REPO/commits/$rcsha/status" --jq '[.statuses[] | select(.context == "qa/rc-gate")][0].state // "none"' 2>/dev/null) || st="unreadable"
      [ "$st" = success ] || refuse "qa/rc-gate on $RCTAG (${rcsha:0:7}) is '$st', not success"
    fi
    note "not checked here: the images the preview pods run (cluster)"
  fi
fi
finish
