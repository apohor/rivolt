#!/usr/bin/env bash
# Usage: check-no-qa-hook.sh <Rivolt.app> [--expect-present]
# The QA sign-in hook (Rivolt/App/QASignIn.swift, flag QA_SIGNIN, Debug configuration only) must not be in a Release build: no marker string in
# the app bundle. With --expect-present the check is the other way round, to show on a Debug build that it can see the marker at all.
set -euo pipefail
app="${1:?path to Rivolt.app}"
test -d "$app" || { echo "not a directory: $app" >&2; exit 2; }
marker="QA_SIGNIN_HOOK_MARKER"
found=$(grep -rl --binary-files=text "$marker" "$app" 2>/dev/null || true)
if [ "${2:-}" = "--expect-present" ]; then
  [ -n "$found" ] || { echo "the check cannot see the marker in a Debug build: it proves nothing" >&2; exit 1; }
  echo "ok: the marker is in the Debug build ($(echo "$found" | head -1 | xargs basename))"
else
  [ -z "$found" ] || { echo "the QA sign-in hook is in this build:" >&2; echo "$found" >&2; exit 1; }
  echo "ok: no QA sign-in hook in $app"
fi
