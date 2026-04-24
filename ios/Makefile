.PHONY: project open clean

# Regenerate Rivolt.xcodeproj from project.yml. Required whenever you
# add, rename, or delete a source file, or change bundle ID / deploy
# target / build settings.
project:
	@command -v xcodegen >/dev/null 2>&1 || { echo "xcodegen not installed. Run: brew install xcodegen"; exit 1; }
	xcodegen generate

# One-shot: regenerate and open in Xcode.
open: project
	open Rivolt.xcodeproj

# Wipe the generated project + DerivedData for this workspace. Source
# files under Rivolt/ are untouched.
clean:
	rm -rf Rivolt.xcodeproj
	rm -rf ~/Library/Developer/Xcode/DerivedData/Rivolt-*
