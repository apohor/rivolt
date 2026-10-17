package main

import (
	"log/slog"
	"os"
	"testing"
)

func TestDemoOnlyScrubEnvRemovesAuthMailPushAndAI(t *testing.T) {
	gone := []string{
		"KRATOS_ADMIN_URL", "HYDRA_ADMIN_URL", "RIVOLT_OIDC_PROVIDERS", "RIVOLT_OIDC_GOOGLE_CLIENT_SECRET",
		"RIVOLT_RESEND_API_KEY", "RIVOLT_EMAIL_FROM", "VAPID_PRIVATE_KEY", "OPENAI_API_KEY",
		"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "RIVOLT_TRUSTED_PROXY_CIDR", "RIVOLT_AUTH_BYPASS_USER",
	}
	kept := []string{"DATABASE_URL", "RIVOLT_DEMO", "RIVOLT_APP_URL", "RIVOLT_REDIS_KEY_PREFIX", "RIVOLT_TILES_BASE_URL"}
	for _, n := range append(append([]string{}, gone...), kept...) {
		t.Setenv(n, "x")
	}
	demoOnlyScrubEnv(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for _, n := range gone {
		if v, ok := os.LookupEnv(n); ok {
			t.Errorf("%s survived (%q)", n, v)
		}
	}
	for _, n := range kept {
		if os.Getenv(n) != "x" {
			t.Errorf("%s was removed", n)
		}
	}
}
