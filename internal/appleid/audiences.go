package appleid

import "strings"

// AppBundleIDs are the iPhone app's bundle ids from configuration: RIVOLT_APPLE_IAP_BUNDLE_ID, and the bundle part of each
// RIVOLT_IOS_APP_IDS entry ("TEAMID.bundle.id"). Apple's notifications for an account made in the app carry the bundle id as the audience.
func AppBundleIDs(getenv func(string) string) []string {
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		for _, o := range out {
			if o == id {
				return
			}
		}
		out = append(out, id)
	}
	add(getenv("RIVOLT_APPLE_IAP_BUNDLE_ID"))
	for _, e := range strings.Split(getenv("RIVOLT_IOS_APP_IDS"), ",") {
		if _, bundle, ok := strings.Cut(strings.TrimSpace(e), "."); ok {
			add(bundle)
		}
	}
	return out
}
