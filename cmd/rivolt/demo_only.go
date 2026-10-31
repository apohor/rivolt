package main

import (
	"log/slog"
	"os"
	"strings"
)

// demoOnlyEnvPrefixes are the environment families that would bring up a
// sign-in, mail, push or AI subsystem. The standalone public demo has
// none of them, so they are removed before anything reads them: a stray
// secret in the demo's environment can then neither enable a login
// surface nor send mail.
var demoOnlyEnvPrefixes = []string{
	"KRATOS_", "HYDRA_", "RIVOLT_OIDC_", "RIVOLT_RESEND_", "RIVOLT_EMAIL_",
	"VAPID_", "OPENAI_", "ANTHROPIC_", "GEMINI_", "GOOGLE_API_KEY",
	"RIVOLT_AUTH_BYPASS",
}

// demoOnlyScrubEnv unsets every variable under demoOnlyEnvPrefixes and
// logs how many it removed (names only, never values).
func demoOnlyScrubEnv(logger *slog.Logger) {
	var removed []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range demoOnlyEnvPrefixes {
			if strings.HasPrefix(name, p) {
				_ = os.Unsetenv(name)
				removed = append(removed, name)
				break
			}
		}
	}
	logger.Info("demo-only: auth, mail, push and AI environment ignored", "removed", removed)
}
