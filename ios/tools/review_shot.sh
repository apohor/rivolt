#!/bin/bash
# Makes ios/store/review-subscription.png (the App Review screenshot of the Rivolt Pro purchase sheet) with ReviewShotUITests on the
# 6.9 inch simulator. Run from anywhere; needs xcodegen and Xcode. The PNG is then committed in a PR like any other file.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="$PWD/store/review-subscription.png"
xcodegen generate -q
udid=$(xcrun simctl list devices available | sed -n 's/.*iPhone 18 Pro Max (\([0-9A-F-]*\)).*/\1/p' | head -1)
[ -n "$udid" ] || { echo "no iPhone 18 Pro Max simulator"; exit 1; }
RIVOLT_REVIEW_SHOT_OUT="$OUT" TEST_RUNNER_RIVOLT_REVIEW_SHOT_OUT="$OUT" xcodebuild test -project Rivolt.xcodeproj -scheme RivoltUITests \
  -destination "id=$udid" -only-testing:RivoltUITests/ReviewShotUITests CODE_SIGNING_ALLOWED=NO -quiet
file "$OUT"
