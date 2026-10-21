#!/bin/bash
# Checks a built Rivolt.app for what App Store validation rejected in
# ios-v0.1.0/0.1.1: the app icon (CFBundleIconName + a compiled 120x120 icon)
# and iPhone-only UIDeviceFamily. Used by ios.yml (unsigned Release build on
# every PR) and ios-testflight.yml (the signed archive, before upload).
#
# usage: check-app-store-basics.sh <path/to/Rivolt.app> [derived-data dir]
# On failure it prints the app's Info.plist and the asset catalog's generated
# plist from the derived data, so the cause is readable in the CI log.
set -uo pipefail
app=$1
dd=${2:-$HOME/Library/Developer/Xcode/DerivedData}
plist="$app/Info.plist"
pb() { /usr/libexec/PlistBuddy -c "Print :$1" "$plist" 2>/dev/null; }

fail=0
# actool writes the icon name under CFBundleIcons, which is where App Store
# validation looks for it.
[ "$(pb CFBundleIcons:CFBundlePrimaryIcon:CFBundleIconName)" = AppIcon ] \
  || { echo "FAIL: CFBundleIcons.CFBundlePrimaryIcon.CFBundleIconName is not AppIcon"; fail=1; }
{ [ "$(pb UIDeviceFamily:0)" = 1 ] && ! pb UIDeviceFamily:1 >/dev/null; } \
  || { echo "FAIL: UIDeviceFamily is not iPhone-only"; fail=1; }
# Embedded extensions (the notification service) are built per target too.
for ext in "$app"/PlugIns/*.appex; do
  [ -d "$ext" ] || continue
  f=$(/usr/libexec/PlistBuddy -c "Print :UIDeviceFamily" "$ext/Info.plist" 2>/dev/null | tr -d ' \n')
  [ "$f" = "Array{1}" ] || { echo "FAIL: $(basename "$ext") UIDeviceFamily is not iPhone-only: $f"; fail=1; }
done
ls "$app"/AppIcon60x60@2x.png >/dev/null 2>&1 || { echo "FAIL: no compiled 120x120 icon (AppIcon60x60@2x.png)"; fail=1; }

if [ "$fail" = 0 ]; then
  echo "App Store basics OK: icon name AppIcon, iPhone only, 120x120 icon present"
  exit 0
fi
echo "--- $plist"
plutil -p "$plist" || true
echo "--- app bundle"
ls -la "$app" || true
echo "--- asset catalog output (derived data)"
find "$dd" -name 'assetcatalog_generated_info.plist' -path '*Rivolt*' 2>/dev/null \
  | while read -r f; do echo "$f"; plutil -p "$f"; done
find "$dd" -name 'assetcatalog_dependencies*' -path '*Rivolt*' 2>/dev/null | head -5
exit 1
