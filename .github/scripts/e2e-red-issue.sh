#!/usr/bin/env bash
# One issue titled "e2e red on main" (label: ci), never a pile. Shared by build.yml (WebKit on main pushes)
# and e2e-nightly.yml. A red run opens it or comments on the open one; a green run comments that it is green
# again, once per kind of run (a main push lands often, so it does not repeat itself). The lead closes it.
#
# Env: RESULT (success or failure), KIND (which run, as it reads in the comment), RUN_ID, RUN_URL, TITLE,
# ARTIFACTS (the artifact names to point at), GH_TOKEN, GH_REPO.
set -euo pipefail

open=$(gh issue list --state open --label ci --search "\"$TITLE\" in:title" --json number,title \
  --jq "[.[] | select(.title == env.TITLE)][0].number // empty")

if [ "$RESULT" = "success" ]; then
  [ -n "$open" ] || exit 0
  green="The $KIND run is green: "
  last=$(gh issue view "$open" --json comments --jq '.comments[-1].body // ""')
  case "$last" in "$green"*) echo "issue #$open already says the $KIND run is green"; exit 0 ;; esac
  gh issue comment "$open" --body "$green$RUN_URL"
  exit 0
fi

failed=$(gh run view "$RUN_ID" --json jobs --jq '.jobs[] | select(.conclusion == "failure" and (.name | startswith("e2e"))) | "- " + .name')
body=$(printf 'The %s e2e run on main failed: %s\n\nFailed jobs:\n%s\n\nThe artifacts (%s) hold the report, screenshots and error context for 7 days.' \
  "$KIND" "$RUN_URL" "${failed:-"- (see the run)"}" "$ARTIFACTS")
if [ -n "$open" ]; then
  gh issue comment "$open" --body "$body"
else
  gh label create ci --color B60205 --description "CI health" 2>/dev/null || true
  gh issue create --title "$TITLE" --label ci --body "$body"
fi
