#!/usr/bin/env bash
# preview-tags-agree.sh PREVIEW_VALUES ADMIN_VALUES [check]: set the admin preview's image tag to the preview's, whatever the admin's old value was, and
# fail unless the two end up equal. The preview bump rewrites only `tag: sha-...`, so an admin tag left on a release candidate (quoted) was skipped
# in silence and the two sites ran different builds (RivoltVersionSkew, Nov 21). Each file must hold exactly one tag line. With `check` nothing
# is written: it only fails when the two differ.
set -euo pipefail
pre=${1:?preview values file}; adm=${2:?admin preview values file}
re='^[[:space:]]+tag:[[:space:]]'
tag_of() {
  local n
  n=$(grep -cE "$re" "$1" || true)
  [ "$n" = 1 ] || { echo "::error::$1: expected exactly one tag line, found $n" >&2; return 1; }
  grep -E "$re" "$1" | sed -E 's/^[[:space:]]+tag:[[:space:]]+//; s/[[:space:]]*(#.*)?$//'
}
want=$(tag_of "$pre")
got=$(tag_of "$adm")
hint="repair: set rivolt-admin-preview's tag to rivolt-preview's in one PR to rivolt-infra (a bump to a newer build also does it); while main is skewed, merge-infra.sh refuses every other infra PR"
if [ "${3:-}" = check ]; then
  # Quotes are not part of the tag: "1.3.1-rc.1" and 1.3.1-rc.1 are the same image.
  [ "${got//\"/}" = "${want//\"/}" ] || { echo "::error::admin preview tag is '$got', preview's is '$want'; $hint" >&2; exit 1; }
  echo "preview and admin preview both at $want"; exit 0
fi
# `|` as the delimiter: a tag holds no `|`. The indent and the key stay as they are.
sed -i.bak -E "s|^([[:space:]]+tag:[[:space:]]+).*\$|\1${want}|" "$adm" && rm -f "$adm.bak"
got=$(tag_of "$adm")
[ "$got" = "$want" ] || { echo "::error::admin preview tag is '$got', preview's is '$want'; $hint" >&2; exit 1; }
echo "preview and admin preview both at $want"
