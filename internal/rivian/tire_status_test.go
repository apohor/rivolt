package rivian

import (
	"bytes"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type tireCount map[string]int

func (c tireCount) OnSleepEnded(string, time.Duration, bool) {}
func (c tireCount) OnTireStatus(class string)                { c[class]++ }

func TestTireStatusClassIsBounded(t *testing.T) {
	for in, want := range map[string]string{
		"Low": "low", " LOW ": "low", "critically low": "low", "critical-low": "low", "Very Low": "low", "low_pressure": "low",
		"Normal": "not_low", "ok": "not_low", "": "missing", "  ": "missing",
		"lowish": "other", "unknown": "other", "undefined": "other", "<script>": "other",
	} {
		if got := tireStatusClass(in); got != want {
			t.Errorf("tireStatusClass(%q) = %q, want %q", in, got, want)
		}
	}
	if TireStatusLow("Normal") || !TireStatusLow("Critically Low") || TireStatusLow("") {
		t.Error("TireStatusLow disagrees with the classes")
	}
}

// The three copies of the low words (this package, the web's Truck card and the app's) must be the same list.
func TestTireLowWordsMatchTheClients(t *testing.T) {
	want := make([]string, 0, len(tireLowWords))
	for w := range tireLowWords {
		want = append(want, w)
	}
	sort.Strings(want)
	for file, re := range map[string]*regexp.Regexp{
		"../../web/src/lib/truckHealth.ts":        regexp.MustCompile(`RIVIAN_LOW_WORDS = new Set\(\[([^\]]*)\]`),
		"../../ios/Rivolt/Views/TruckLogic.swift": regexp.MustCompile(`rivianLowWords: Set<String> = \[([^\]]*)\]`),
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		m := re.FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: the low words list was not found (renamed? update this test)", file)
		}
		var got []string
		for _, w := range strings.Split(string(m[1]), ",") {
			if w = strings.Trim(strings.TrimSpace(w), `"`); w != "" {
				got = append(got, w)
			}
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s has %v, the Go set has %v", file, got, want)
		}
	}
}

func TestObserveTiresCountsPerWheelAndLogsAnOddWordOnce(t *testing.T) {
	var logs bytes.Buffer
	counts := tireCount{}
	m := &StateMonitor{logger: slog.New(slog.NewTextHandler(&logs, nil)), sleepObserver: counts}
	m.observeTires(&State{}) // no tire information: nothing
	m.observeTires(&State{OtaStatus: "Idle"})
	m.observeTires(&State{TirePressureStatusFL: "Normal", TirePressureStatusFR: "Normal", TirePressureStatusRL: "Low", TirePressureStatusRR: "Mystery"})
	m.observeTires(&State{TirePressureFLBar: 2.9, TirePressureStatusRR: "Mystery"}) // FL has a pressure and no status: missing
	if counts["not_low"] != 2 || counts["low"] != 1 || counts["other"] != 2 || counts["missing"] != 1 {
		t.Errorf("counts = %v", counts)
	}
	if n := strings.Count(logs.String(), "tire status word seen"); n != 1 || !strings.Contains(logs.String(), "status=Mystery") {
		t.Errorf("want one line for Mystery:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "vehicle") {
		t.Errorf("the log carries a vehicle: %s", logs.String())
	}
}

func TestObserveTiresIgnoresFreeTextAndBoundsWhatItKeeps(t *testing.T) {
	var logs bytes.Buffer
	m := &StateMonitor{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	m.observeTires(&State{TirePressureStatusFL: "see https://evil.example/?token=abc"})
	if strings.Contains(logs.String(), "evil") || strings.Contains(logs.String(), "token") {
		t.Errorf("free text reached the log: %s", logs.String())
	}
	for i := 0; i < 100; i++ {
		m.observeTires(&State{TirePressureStatusFL: "word" + strings.Repeat("x", i%10) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))})
	}
	if len(m.tireWords) > tireMaxWords {
		t.Errorf("remembered %d words, want at most %d", len(m.tireWords), tireMaxWords)
	}
	(*StateMonitor)(nil).observeTires(&State{TirePressureStatusFL: "x"}) // never panics
}

// The word table, pinned: every low word and every not-low word the alert knows. A change here is a change to what can alert,
// so it is a deliberate edit of this list (and of the web and iPhone copies TestTireLowWordsMatchTheClients keeps in step).
// Any word not listed is no claim: it never alerts and never clears.
func TestTheTireWordTableIsPinned(t *testing.T) {
	wantLow := []string{"critical", "critical_low", "critically_low", "low", "low_pressure", "very_low", "warning_hard", "warning_puncture", "warning_soft"}
	if got := TireLowWords(); strings.Join(got, ",") != strings.Join(wantLow, ",") {
		t.Errorf("low words = %v, want %v", got, wantLow)
	}
	for _, w := range wantLow {
		if !TireStatusLow(w) || TireStatusNotLow(w) || TireLowWord(w) != w {
			t.Errorf("%q: not read as low", w)
		}
	}
	for _, w := range []string{"normal", "ok"} {
		if TireStatusLow(w) || !TireStatusNotLow(w) {
			t.Errorf("%q: not read as not-low", w)
		}
	}
	for _, w := range []string{"", "unknown", "squishy", "lowish", "Low Pressure!", "no_data", "unspecified", "tire_pressure_status_unspecified"} {
		if TireStatusLow(w) && w != "" {
			t.Errorf("%q alerts but is not in the table", w)
		}
	}
	// Spacing and case are normalised, so the table lists words and not their spellings.
	if !TireStatusLow(" Very-Low ") || TireLowWord(" Very-Low ") != "very_low" {
		t.Error("spacing, hyphens and case are not normalised")
	}
}
