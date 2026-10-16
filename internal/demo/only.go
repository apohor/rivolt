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

// MisconfiguredOnly reports RIVOLT_DEMO_ONLY=true without the replay demo
// it depends on. The caller refuses to boot: serving a half-demo install
// to the public is worse than not starting.
func MisconfiguredOnly() bool {
	return envTrue("RIVOLT_DEMO_ONLY") && !Enabled()
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
