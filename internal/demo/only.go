package demo

import (
	"net/url"
	"os"
	"strings"
)

// OnlyEnabled reports whether this install is the standalone public demo
// (demo.rivolt.dev): RIVOLT_DEMO_ONLY=true on an install where Enabled
// holds. It is a strict subset of Enabled, so a prod install (live
// client) can never turn it on even if the variable leaks into its
// environment. In this mode the process serves nothing but the demo: no
// sign-in, signup, mail, push or events, and every /api route outside the
// demo allowlist is a 404.
func OnlyEnabled() bool {
	return Enabled() && envTrue("RIVOLT_DEMO_ONLY")
}

// MisconfiguredOnly reports a half-configured standalone demo:
// RIVOLT_DEMO_ONLY=true and RIVOLT_DEMO=true, but not the replay sample
// setup the demo needs. The caller refuses to boot: serving a half-demo
// install to the public is worse than not starting.
func MisconfiguredOnly() bool {
	return envTrue("RIVOLT_DEMO_ONLY") && envTrue("RIVOLT_DEMO") && !Enabled()
}

// StrayOnly reports RIVOLT_DEMO_ONLY=true without RIVOLT_DEMO=true: not a
// demo install at all, just a stray variable. It is ignored (the caller
// logs a loud warning) rather than refused, so it can never take a real
// install down.
func StrayOnly() bool {
	return envTrue("RIVOLT_DEMO_ONLY") && !envTrue("RIVOLT_DEMO")
}

func envTrue(name string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(name)), "true")
}

// AppURL is the real app's origin (RIVOLT_APP_URL), where the standalone
// demo's sign-in and sign-up links point. Only a plain http(s) origin is
// accepted (no credentials, query or fragment); anything else is "" so
// the config never carries a link the operator didn't mean. A trailing
// slash and any path are dropped.
func AppURL() string {
	raw := strings.TrimSpace(os.Getenv("RIVOLT_APP_URL"))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
