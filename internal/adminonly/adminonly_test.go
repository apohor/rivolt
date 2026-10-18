package adminonly

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var good = map[string]string{
	"RIVOLT_ADMIN_ONLY":            "true",
	"RIVOLT_ACCESS_TEAM_DOMAIN":    "jolly-river-3a16.cloudflareaccess.com",
	"RIVOLT_ACCESS_AUD":            "aud",
	"RIVOLT_ADMIN_EMAILS":          "owner@example.com",
	"RIVIAN_CLIENT":                "stub",
	"RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true",
}

func with(k, v string) map[string]string {
	m := map[string]string{}
	for a, b := range good {
		m[a] = b
	}
	m[k] = v
	return m
}

func TestGoodEnvHasNoProblems(t *testing.T) {
	if !Enabled(env(good)) {
		t.Fatal("not enabled")
	}
	if p := Problems(env(good)); len(p) != 0 {
		t.Fatalf("problems: %v", p)
	}
	if Enabled(env(map[string]string{"RIVOLT_ADMIN_ONLY": "false"})) {
		t.Fatal("false enabled it")
	}
}

func TestRefusesMissingAccessSettings(t *testing.T) {
	for _, k := range []string{"RIVOLT_ACCESS_TEAM_DOMAIN", "RIVOLT_ACCESS_AUD", "RIVOLT_ADMIN_EMAILS"} {
		p := Problems(env(with(k, "  ")))
		if len(p) != 1 || !strings.Contains(p[0], k) {
			t.Errorf("empty %s: %v", k, p)
		}
	}
}

func TestRefusesWorkerAndPublicEnv(t *testing.T) {
	for _, k := range []string{
		"RIVOLT_TRUSTED_PROXY_CIDR", "RIVOLT_AUTH_BYPASS_USER", "RIVOLT_DEMO", "RIVOLT_DEMO_ONLY",
		"RIVOLT_REPLAY_SOURCE", "RIVOLT_POD_ID", "RIVOLT_DRAIN_METRICS_INTERVAL",
		"RIVOLT_ENGAGEMENT_EMAILS", "RIVOLT_READONLY_ROLE",
	} {
		p := Problems(env(with(k, "x")))
		if len(p) != 1 || !strings.Contains(p[0], k) {
			t.Errorf("%s set: %v", k, p)
		}
	}
	if p := Problems(env(with("RIVOLT_ADMIN_DISABLED", "true"))); len(p) != 1 {
		t.Errorf("ADMIN_DISABLED: %v", p)
	}
}

func TestRefusesAnythingButStubAndLiveForbidden(t *testing.T) {
	for _, c := range []string{"", "live", "mock", "replay", "Live "} {
		p := Problems(env(with("RIVIAN_CLIENT", c)))
		if len(p) != 1 || !strings.Contains(p[0], "RIVIAN_CLIENT") {
			t.Errorf("RIVIAN_CLIENT=%q: %v", c, p)
		}
	}
	if p := Problems(env(with("RIVIAN_CLIENT", " STUB "))); len(p) != 0 {
		t.Errorf("stub in any case is fine: %v", p)
	}
	for _, v := range []string{"", "false", "0", "no", "off", "maybe"} {
		p := Problems(env(with("RIVOLT_RIVIAN_LIVE_FORBIDDEN", v)))
		if len(p) != 1 || !strings.Contains(p[0], "LIVE_FORBIDDEN") {
			t.Errorf("RIVOLT_RIVIAN_LIVE_FORBIDDEN=%q: %v", v, p)
		}
	}
	for _, v := range []string{"true", "TRUE", "1", "yes", "on"} {
		if p := Problems(env(with("RIVOLT_RIVIAN_LIVE_FORBIDDEN", v))); len(p) != 0 {
			t.Errorf("RIVOLT_RIVIAN_LIVE_FORBIDDEN=%q should pass: %v", v, p)
		}
	}
}
