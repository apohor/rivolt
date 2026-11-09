#!/usr/bin/env bash
# Usage: check-no-qa-hook.sh <Rivolt.app> [--expect-present]
# The QA sign-in hook (Rivolt/App/QASignIn.swift, flag QA_SIGNIN, Debug configuration only) must not be in a Release build: no marker string or
# launch variable name of it in the app bundle. With --expect-present the check is the other way round, to show on a Debug build that it can see the marker at all.
set -euo pipefail
app="${1:?path to Rivolt.app}"
test -d "$app" || { echo "not a directory: $app" >&2; exit 2; }
# The marker, the names of the launch variables, and the UI-test fixture server: none of them is in a build without them (Release).
found=$(grep -rlE --binary-files=text "QA_SIGNIN_HOOK_MARKER|RIVOLT_QA_TOKEN|RIVOLT_QA_SERVER|RIVOLT_UITEST_FIXTURES|RIVOLT_UITEST_NO_TRUCK|RIVOLT_UITEST_TRACE|FixtureURLProtocol" "$app" 2>/dev/null || true)
if [ "${2:-}" = "--expect-present" ]; then
  [ -n "$found" ] || { echo "the check cannot see the marker in a Debug build: it proves nothing" >&2; exit 1; }
  echo "ok: the marker is in the Debug build ($(echo "$found" | head -1 | xargs basename))"
else
  [ -z "$found" ] || { echo "the QA sign-in hook is in this build:" >&2; echo "$found" >&2; exit 1; }
  echo "ok: no QA sign-in hook in $app"
fi
