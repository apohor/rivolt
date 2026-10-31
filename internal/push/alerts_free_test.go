package push

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Alerts are free for every vehicle of every plan: left-open, charging done, the plug-in reminder, anomalies and software updates are
// delivered by user, not by vehicle, and never ask the plan. The only place the push package consults it is the routine Live Activity
// update (sendLive); this fails if any other path starts to.
func TestOnlyLiveActivityConsultsThePlan(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(err, files)
	}
	uses := 0
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
			t.Errorf("%s imports the entitlements package: alerts must not depend on the plan", f)
		}
		if n := strings.Count(src, "s.isPro"); n > 0 {
			if f != "apns_live.go" {
				t.Errorf("%s consults the plan (s.isPro); only the Live Activity update may", f)
			}
			uses += n
		}
	}
	// The setter (WithProCheck) plus the nil test and the call in sendLive: if this grows, a new path asks the plan.
	if uses != 3 {
		t.Errorf("s.isPro is used %d times in the push package, want 3 (the setter, the nil test and the call in sendLive)", uses)
	}
}
