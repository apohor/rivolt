package push

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Alerts and the Live Activity are free for every vehicle of every plan: left-open, charging done, the plug-in reminder, anomalies, software
// updates and the charging Live Activity are delivered by user, not by vehicle, and never ask the plan. Nothing in the push package consults
// it; this fails if any path starts to.
func TestPushNeverConsultsThePlan(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(err, files)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if strings.Contains(src, "internal/entitlements") {
			t.Errorf("%s imports the entitlements package: pushes must not depend on the plan", f)
		}
		for _, bad := range []string{"isPro", "WithProCheck", "IsPro"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s mentions %s: pushes must not ask the plan", f, bad)
			}
		}
	}
}

// A running Live Activity changes nothing about how a truck is polled: the Rivian client and monitor know nothing of activities, so no Rivian
// call or poll rate can be keyed on one (the figures come from the monitor's latest state, read-only, in cmd/rivolt/main.go).
func TestRivianPackageKnowsNoLiveActivity(t *testing.T) {
	files, err := filepath.Glob("../rivian/*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(err, files)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if strings.Contains(src, "rivolt/internal/push") {
			t.Errorf("%s imports the push package", f)
		}
		for _, bad := range []string{"LiveActivity", "ListLiveActivities", "ActivityCount", "RunLive", "UpdateLive"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s mentions %s: a Live Activity must not change how Rivian is called", f, bad)
			}
		}
	}
}
