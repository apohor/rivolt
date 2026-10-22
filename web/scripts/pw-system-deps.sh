#!/usr/bin/env bash
# System libraries for a Playwright browser already in ~/.cache/ms-playwright, installed only when one is missing.
#
# `playwright install-deps` always runs apt (an update, then every font and library it wants), and on a shared runner
# the Ubuntu mirror is sometimes slow: 26 MB at 96 kB/s took 4.5 minutes of a 3-minute shard. ubuntu-latest already
# carries the libraries chromium links against, so check the binaries and call apt only if the dynamic linker says a
# library is missing. (What install-deps would add on top is CJK and other fallback fonts: no spec renders them.)
# WebKit's libraries are not on the image: it always installs.
#
# usage: pw-system-deps.sh <chromium|webkit>
set -euo pipefail
project="${1:?usage: pw-system-deps.sh <chromium|webkit>}"

if [ "$project" = "chromium" ]; then
  missing=0
  found=0
  while IFS= read -r bin; do
    found=1
    if ldd "$bin" 2>/dev/null | grep -q 'not found'; then
      echo "$bin: missing libraries:"; ldd "$bin" | grep 'not found'
      missing=1
    fi
  done < <(find "$HOME/.cache/ms-playwright" -maxdepth 3 -type f \( -name chrome -o -name headless_shell \) 2>/dev/null)
  if [ "$found" = 1 ] && [ "$missing" = 0 ]; then
    echo "chromium's system libraries are present; skipping apt"
    exit 0
  fi
fi

npx playwright install-deps "$project"
