#!/usr/bin/env bash
# Builds the web asset carry directory for the image (zero-impact deploys, G4a).
#
#   scripts/web-carry.sh <dist-dir> <out-dir>
#
# During a rolling update a browser can load index.html from a pod of one build and then ask a pod of another for its hashed /assets/ chunks.
# The SPA is embedded in the binary, so each pod only has its own. This collects the hashed assets of the builds a browser may still be on, into
# <out-dir>/assets (plus <out-dir>/MANIFEST.json), which the image ships at /app/web-carry and the server reads for a chunk it lacks:
#   - this build's own assets (<dist-dir>/assets);
#   - the main builds of the last WINDOW_HOURS (48): the previous main image's carry, kept by each file's age;
#   - the last two final releases, whatever their age (prod can sit on one for days): their images' carry, whole.
# Each source is an image from the registry; its /app/web-carry is copied out with `docker create` + `docker cp` (nothing is run). Hashed names
# never collide, so a file already collected is left alone. A source that cannot be copied (no network, the image is gone, or it predates the
# carry) does NOT fail the build: it is listed under "missing" in the manifest, the server exports its count as
# rivolt_web_carry_missing_sources, and a ::warning:: annotation is printed.
#
# Environment (all optional):
#   REGISTRY_IMAGE  image repository (default ghcr.io/apohor/rivolt)
#   MAIN_TAG        the floating tag of the latest main build (default main)
#   FINAL_TAGS      the final release image tags to take, newest first, space separated; default: the two newest vX.Y.Z git tags without a suffix,
#                   as image tags without the v
#   WINDOW_HOURS    how long a main build's file stays (default 48)
#   DOCKER          the docker command (default docker)
#   NOW             unix time (default now)
set -uo pipefail

dist=${1:?usage: web-carry.sh <dist-dir> <out-dir>}
out=${2:?usage: web-carry.sh <dist-dir> <out-dir>}
image=${REGISTRY_IMAGE:-ghcr.io/apohor/rivolt}
main_tag=${MAIN_TAG:-main}
window=$(( ${WINDOW_HOURS:-48} * 3600 ))
docker_cmd=${DOCKER:-docker}
now=${NOW:-$(date +%s)}

if [ -z "${FINAL_TAGS+x}" ]; then
  FINAL_TAGS=$(git tag --list 'v[0-9]*' --sort=-v:refname 2>/dev/null | grep -v -- '-' | head -2 | sed 's/^v//' | tr '\n' ' ')
fi

mkdir -p "$out/assets"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
files="$work/files.tsv"   # name<TAB>unix time
: > "$files"
missing=()
sources=()

# keep NAME TIME: copy SRC/assets/NAME into out unless already there; remember the newest time.
add_file() { # src name time
  if [ ! -e "$out/assets/$2" ]; then cp -p "$1/assets/$2" "$out/assets/$2"; fi
  printf '%s\t%s\n' "$2" "$3" >> "$files"
}

# 1. this build's own assets
if [ -d "$dist/assets" ]; then
  for f in "$dist"/assets/*; do
    [ -f "$f" ] || continue
    add_file "$dist" "$(basename "$f")" "$now"
  done
fi

# fetch REF DIR: the image's /app/web-carry in DIR; false when it cannot.
fetch() {
  local ref=$1 dir=$2 cid
  "$docker_cmd" pull -q "$ref" >/dev/null 2>&1 || return 1
  cid=$("$docker_cmd" create "$ref" 2>/dev/null) || return 1
  "$docker_cmd" cp "$cid:/app/web-carry" "$dir" >/dev/null 2>&1
  local rc=$?
  "$docker_cmd" rm "$cid" >/dev/null 2>&1 || true
  return $rc
}

# take MODE LABEL REF: mode chain keeps files younger than the window (by the manifest's time), mode final keeps all.
take() {
  local mode=$1 label=$2 ref=$3 dir="$work/src-$2"
  if ! fetch "$ref" "$dir"; then
    missing+=("$label")
    echo "::warning::web carry: could not take the assets of $ref (the image is not reachable, or it predates the carry); building without them"
    return
  fi
  sources+=("$label")
  local name t
  if [ -f "$dir/MANIFEST.json" ] && command -v jq >/dev/null; then
    while IFS=$'\t' read -r name t; do
      [ -f "$dir/assets/$name" ] || continue
      case "$name" in */*|"") continue ;; esac
      if [ "$mode" = chain ] && [ $(( now - t )) -gt "$window" ]; then continue; fi
      add_file "$dir" "$name" "$t"
    done < <(jq -r '.files | to_entries[] | "\(.key)\t\(.value)"' "$dir/MANIFEST.json" | sed 's#^assets/##')
  else
    # No manifest: take the files at the age of the build (final) or not at all (chain: age unknown).
    [ "$mode" = final ] || return
    for f in "$dir"/assets/*; do [ -f "$f" ] && add_file "$dir" "$(basename "$f")" "$now"; done
  fi
}

take chain "$main_tag" "$image:$main_tag"
# No final tags (a checkout without tags, or none made yet) would carry no release at all, and say nothing: count it as missing so the gauge shows it.
if [ -z "$(echo $FINAL_TAGS)" ]; then
  missing+=("no final tags found")
  echo "::warning::web carry: no final release tags found (a checkout without tags?); building without the releases' assets"
fi
for t in $FINAL_TAGS; do
  take final "$t" "$image:$t"
done

# The manifest: each file once, at its newest time.
sort -t$'\t' -k1,1 -k2,2nr "$files" | awk -F'\t' '!seen[$1]++' > "$work/uniq.tsv"
{
  printf '{"built_at":%s,' "$now"
  printf '"sources":%s,' "$(printf '%s\n' "${sources[@]:-}" | jq -R . | jq -s 'map(select(. != ""))')"
  printf '"missing":%s,' "$(printf '%s\n' "${missing[@]:-}" | jq -R . | jq -s 'map(select(. != ""))')"
  printf '"files":%s}\n' "$(jq -R -s 'split("\n") | map(select(length>0) | split("\t")) | map({key: ("assets/" + .[0]), value: (.[1]|tonumber)}) | from_entries' "$work/uniq.tsv")"
} > "$out/MANIFEST.json"

echo "web carry: $(wc -l < "$work/uniq.tsv" | tr -d ' ') files, sources: ${sources[*]:-none}, missing: ${missing[*]:-none}"
exit 0
