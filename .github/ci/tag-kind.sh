#!/usr/bin/env bash
# tag-kind.sh TAG: what a release tag is. Prints "final" for vX.Y.Z (a release that is promoted to prod) or "rc" for
# vX.Y.Z-rc.N (a release candidate: published as an image and pinned to preview for the gate, never promoted). Anything
# else (v1.2.3-beta, v1.2.3-rc, v1.2.3-rc.0, v01.2.3, v1.2.3+meta) prints "invalid" to stderr and exits 1, so the tag run
# stops before it can retag or bump anything. build.yml's tag-validate runs it; tagkind_test.go pins the table.
set -euo pipefail
t=${1:-}
num='(0|[1-9][0-9]*)'
if [[ $t =~ ^v$num\.$num\.$num$ ]]; then
  echo final
elif [[ $t =~ ^v$num\.$num\.$num-rc\.[1-9][0-9]*$ ]]; then
  echo rc
else
  echo "invalid" >&2
  exit 1
fi
