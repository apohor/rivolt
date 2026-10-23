package rivian

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type otaCount map[string]int

func (c otaCount) OnSleepEnded(string, time.Duration, bool) {}
func (c otaCount) OnOTAStatus(class string)                 { c[class]++ }

func TestOTAStatusClassIsBounded(t *testing.T) {
	for in, want := range map[string]string{
		"Idle": "idle", " ready_to_install ": "ready_to_install", "Ready To Install": "ready_to_install", "DOWNLOADING": "downloading",
		"Installing": "installing", "something new": "other", "": "other", "<script>alert(1)</script>": "other",
	} {
		if got := otaStatusClass(in); got != want {
			t.Errorf("otaStatusClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestObserveOTACountsAndLogsWhatIsNew(t *testing.T) {
	var logs bytes.Buffer
	counts := otaCount{}
	m := &StateMonitor{logger: slog.New(slog.NewTextHandler(&logs, nil)), sleepObserver: counts}
	push := func(vehicle, status string, progress float64) {
		m.observeOTA(vehicle, &State{OtaStatus: status, OtaInstallProgress: progress})
	}
	push("v1", "Idle", 0)
	push("v1", "Idle", 0)
	push("v1", "", 0)              // no OTA information: nothing
	push("v1", "Downloading", 0.4) // first non-zero progress, on a 0 to 1 scale
	push("v1", "Downloading", 0.5)
	push("v2", "Downloading", 40) // above 1: the scale is 0 to 100, and v2's first non-zero too
	push("v2", "Mystery State", 41)
	if counts["idle"] != 2 || counts["downloading"] != 3 || counts["other"] != 1 {
		t.Errorf("counts = %v", counts)
	}
	out := logs.String()
	for _, want := range []string{
		`msg="ota status seen" vehicle=v1 status=Idle class=idle`,
		`msg="ota status seen" vehicle=v1 status=Downloading class=downloading`,
		`msg="ota status seen" vehicle=v2 status="Mystery State" class=other`,
		`msg="ota install progress seen" vehicle=v1 progress=0.4 status=Downloading`,
		`msg="ota install progress seen" vehicle=v2 progress=40 status=Downloading`,
		`msg="ota install progress above 1 (so a 0 to 100 scale)" vehicle=v2 progress=40`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	// Each is logged once: v1's second progress and v2's second one add nothing.
	for _, never := range []string{`progress=0.5`, `progress=41`} {
		if strings.Contains(out, never) {
			t.Errorf("%q was logged again", never)
		}
	}
	if n := strings.Count(out, "ota status seen"); n != 3 {
		t.Errorf("%d status lines, want 3 (one per distinct word):\n%s", n, out)
	}
}

func TestObserveOTAIgnoresOddWordsAndBoundsWhatItKeeps(t *testing.T) {
	var logs bytes.Buffer
	m := &StateMonitor{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	m.observeOTA("v1", &State{OtaStatus: "see https://evil.example/?token=abc"})
	if strings.Contains(logs.String(), "evil") || strings.Contains(logs.String(), "token") {
		t.Errorf("free text reached the log: %s", logs.String())
	}
	for i := 0; i < 100; i++ {
		m.observeOTA("v1", &State{OtaStatus: "word" + strings.Repeat("x", i%10) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))})
	}
	if len(m.otaWords) > otaMaxWords {
		t.Errorf("remembered %d words, want at most %d", len(m.otaWords), otaMaxWords)
	}
	(*StateMonitor)(nil).observeOTA("v", &State{OtaStatus: "x"}) // never panics
}
