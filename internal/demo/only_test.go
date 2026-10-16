package demo

import "testing"

func demoEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
}

func TestOnlyEnabledNeedsTheReplayDemo(t *testing.T) {
	t.Setenv("RIVOLT_DEMO_ONLY", "true")
	// A live install with the variable leaked into its environment.
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "live")
	if OnlyEnabled() {
		t.Fatal("demo-only turned on for a live client")
	}
	if !MisconfiguredOnly() {
		t.Error("demo-only without the replay demo must be reported as misconfigured")
	}
	demoEnv(t)
	if !OnlyEnabled() || MisconfiguredOnly() {
		t.Fatal("demo-only off on a replay demo")
	}
	t.Setenv("RIVOLT_DEMO_ONLY", "")
	if OnlyEnabled() || MisconfiguredOnly() {
		t.Error("demo-only on without RIVOLT_DEMO_ONLY")
	}
}

func TestAppURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://rivolt.dev":         "https://rivolt.dev",
		"https://rivolt.dev/":        "https://rivolt.dev",
		"https://rivolt.dev/app/x":   "https://rivolt.dev",
		"http://localhost:8080":      "http://localhost:8080",
		"":                           "",
		"rivolt.dev":                 "",
		"javascript:alert(1)":        "",
		"ftp://rivolt.dev":           "",
		"https://u:p@rivolt.dev":     "",
		"https://rivolt.dev?next=/x": "",
		"https://rivolt.dev/#frag":   "",
		"//rivolt.dev":               "",
		"https://":                   "",
	} {
		t.Setenv("RIVOLT_APP_URL", in)
		if got := AppURL(); got != want {
			t.Errorf("AppURL(%q) = %q, want %q", in, got, want)
		}
	}
}
