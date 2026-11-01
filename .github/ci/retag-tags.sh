#!/usr/bin/env bash
# retag-tags.sh TAG: the image tags a tag push attaches to the already-built sha-<short> image, one per line.
#   vX.Y.Z       -> X.Y.Z, X.Y, latest   (the prod promotion)
#   vX.Y.Z-rc.N  -> X.Y.Z-rc.N           (a release candidate: never X.Y or latest, which would move prod's floating tags)
# An invalid tag fails (tag-kind.sh). retag in build.yml calls it; tagkind_test.go pins both tables.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
t=$1
kind=$("$here/tag-kind.sh" "$t")
v=${t#v}
case "$kind" in
  final) echo "$v"; echo "${v%.*}"; echo latest ;;
  rc)    echo "$v" ;;
esac
