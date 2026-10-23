// Package adminonly is the startup contract of the admin deployment
// (RIVOLT_ADMIN_ONLY=true, admin.rivolt.dev): the same image as the public
// site, serving only the admin surface behind Cloudflare Access, and running
// no background work. The caller refuses to boot when Problems is non-empty:
// a half-configured admin install is worse than none.
package adminonly

import (
	"fmt"
	"strings"
)

// Enabled reports RIVOLT_ADMIN_ONLY=true.
func Enabled(getenv func(string) string) bool { return truthy(getenv("RIVOLT_ADMIN_ONLY")) }

// liveForbidden parses the flag the way rivian.ParseBoolEnv does.
func liveForbidden(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func truthy(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "true") }

// required must be set: they identify the Access app and who may pass it.
var required = []string{"RIVOLT_ACCESS_TEAM_DOMAIN", "RIVOLT_ACCESS_AUD", "RIVOLT_ADMIN_EMAILS"}

// Optional: RIVOLT_ACCESS_SERVICE_TOKENS, a comma list of Cloudflare Access
// service-token client IDs allowed through the outer door without an email
// (QA automation). Empty by default; prod never sets it. A service token still
// needs a Rivolt admin session behind it.

// forbidden are variables that belong to the public site or its workers.
// Each switches on something the admin deployment must not do; setting one
// means the wrong values file reached this release.
var forbidden = []struct{ name, why string }{
	{"RIVOLT_TRUSTED_PROXY_CIDR", "header SSO must stay off (it trusts the ingress IP)"},
	{"RIVOLT_AUTH_BYPASS_USER", "auth bypass"},
	{"RIVOLT_DEMO", "the demo"},
	{"RIVOLT_DEMO_ONLY", "the demo"},
	{"RIVOLT_REPLAY_SOURCE", "the replay client"},
	{"RIVOLT_REPLAY_SOURCE_VEHICLE", "the replay client"},
	{"RIVOLT_REPLAY_BACKFILL", "the replay client"},
	{"RIVOLT_REPLAY_ALLOWED_USERS", "the replay client"},
	{"RIVOLT_POD_ID", "vehicle leases (a worker)"},
	{"RIVOLT_DRAIN_METRICS_INTERVAL", "the drain collector (a worker)"},
	{"RIVOLT_ENGAGEMENT_EMAILS", "engagement mail and the inactive purge (workers)"},
	{"RIVOLT_READONLY_ROLE", "the reporting-role grant loop (a worker)"},
	{"RIVOLT_APP_ROLE", "the request-role grant loop (a worker)"},
}

// Problems lists everything wrong with the environment for an admin-only
// start: missing Access settings, and variables of the public site or its
// workers. Empty means good to go.
func Problems(getenv func(string) string) []string {
	var out []string
	for _, name := range required {
		if strings.TrimSpace(getenv(name)) == "" {
			out = append(out, fmt.Sprintf("%s is required (Cloudflare Access must front the admin site)", name))
		}
	}
	for _, f := range forbidden {
		if strings.TrimSpace(getenv(f.name)) != "" {
			out = append(out, fmt.Sprintf("%s must not be set on the admin deployment: %s", f.name, f.why))
		}
	}
	if truthy(getenv("RIVOLT_ADMIN_DISABLED")) {
		out = append(out, "RIVOLT_ADMIN_DISABLED contradicts RIVOLT_ADMIN_ONLY")
	}
	// The admin site never talks to Rivian: the client must be the stub, and
	// a live client must be forbidden outright, so a stray value cannot turn
	// one on. (Its per-user Sync and Refresh session answer 409 for this.)
	if c := strings.ToLower(strings.TrimSpace(getenv("RIVIAN_CLIENT"))); c != "stub" {
		out = append(out, fmt.Sprintf("RIVIAN_CLIENT must be stub on the admin deployment (got %q)", c))
	}
	if !liveForbidden(getenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN")) {
		out = append(out, "RIVOLT_RIVIAN_LIVE_FORBIDDEN=true is required on the admin deployment")
	}
	return out
}
