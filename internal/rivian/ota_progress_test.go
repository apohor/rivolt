package rivian

import (
	"math"
	"testing"
)

func TestOTAInstallPercentOnlyWhileInstalling(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		progress float64
		want     int // -1: nil
	}{
		{"installing, partway", "installing", 42, 42},
		{"installing, just started", "installing", 0, 0},
		{"the word in any case and spacing", " Installing ", 7, 7},
		{"rounded to a whole percent", "installing", 41.6, 42},
		{"clamped above 100", "installing", 180, 100},
		{"clamped below 0", "installing", -3, 0},
		{"idle", "idle", 100, -1},
		{"ready to install", "ready_to_install", 100, -1},
		{"downloading", "downloading", 55, -1},
		{"a word we do not know", "verifying", 90, -1},
		{"no status", "", 50, -1},
		{"not a number", "installing", math.NaN(), -1},
		{"infinite", "installing", math.Inf(1), -1},
	}
	for _, c := range cases {
		got := OTAInstallPercent(c.status, c.progress)
		if c.want < 0 {
			if got != nil {
				t.Errorf("%s: got %d, want none", c.name, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("%s: got %v, want %d", c.name, got, c.want)
		}
	}
}

// vehicleState pushes only what changed, so the merged state carries the last non-zero progress. A status other than installing must end it,
// or the next install's first frames (status installing, progress not yet sent) would show the previous install's value.
func TestMergeStateEndsTheInstallProgressWithTheInstall(t *testing.T) {
	installing := &State{OtaStatus: "installing", OtaInstallProgress: 80}
	if got := mergeState(installing, &State{OtaInstallProgress: 85}); got.OtaStatus != "installing" || got.OtaInstallProgress != 85 {
		t.Errorf("a progress-only frame: %q %v", got.OtaStatus, got.OtaInstallProgress)
	}
	if got := mergeState(installing, &State{BatteryLevelPct: 61}); got.OtaInstallProgress != 80 {
		t.Errorf("a frame with no OTA fields changed the progress: %v", got.OtaInstallProgress)
	}
	for _, word := range []string{"idle", "installed", "failed", "something_new"} {
		got := mergeState(installing, &State{OtaStatus: word})
		if got.OtaInstallProgress != 0 {
			t.Errorf("status %q left progress %v behind", word, got.OtaInstallProgress)
		}
	}
	// the next install starts from nothing, not from the last one
	idle := mergeState(installing, &State{OtaStatus: "idle"})
	next := mergeState(idle, &State{OtaStatus: "installing"})
	if next.OtaInstallProgress != 0 {
		t.Errorf("a new install started at %v", next.OtaInstallProgress)
	}
	// a frame that names a status and a progress together keeps the progress
	if got := mergeState(installing, &State{OtaStatus: "installing", OtaInstallProgress: 90}); got.OtaInstallProgress != 90 {
		t.Errorf("status and progress together: %v", got.OtaInstallProgress)
	}
}

// One case per word seen on prod trucks (and in Rivian's own app): each maps to its step key; idle and no status are nothing; a word we have
// not seen, including the app's Download_Failed and Install_Failed, reads "unknown" (clients show a quiet "Updating…").
func TestOTAStepPerWord(t *testing.T) {
	for word, want := range map[string]string{
		"Ready_To_Download":    "ready_to_download",
		"Downloading":          "downloading",
		"Preparing":            "preparing",
		"Ready_To_Install":     "ready_to_install",
		"Scheduled_To_Install": "scheduled_to_install",
		"Install_Countdown":    "install_countdown",
		"Awaiting_Install":     "awaiting_install",
		"Installing":           "installing",
		"Install_Success":      "install_success",
		"Connection_Lost":      "connection_lost",
		"INSTALL_SUCCESS":      "install_success", // case and spacing do not matter
		" install success ":    "install_success",
		"Idle":                 "",
		"":                     "",
		"   ":                  "",
		"Install_Failed":       "unknown", // in Rivian's app, never seen on prod: not named
		"Download_Failed":      "unknown",
		"Something_New":        "unknown",
		"<script>":             "unknown",
	} {
		if got := OTAStep(word); got != want {
			t.Errorf("OTAStep(%q) = %q, want %q", word, got, want)
		}
	}
	// the done state is only ever the truck's own Install_Success: not 100% progress, not idle
	if OTAStep("Idle") == "install_success" || OTAStep("Installing") == "install_success" {
		t.Error("install_success from a word other than Install_Success")
	}
	// every named step is a word the counter knows, so none of them lands in "other"
	for k := range OTASteps {
		if otaStatusClass(k) != k {
			t.Errorf("step %q is counted as %q", k, otaStatusClass(k))
		}
	}
}
