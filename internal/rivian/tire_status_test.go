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
		"WARNING_SOFT": "low", " Warning Hard ": "low", "warning-puncture": "low",
		"Normal": "not_low", "TIRE_PRESSURE_STATUS_NORMAL": "other", "": "missing", "  ": "missing",
		"unknown": "other", "unspecified": "other", "low": "other", "critical": "other", "ok": "other", "lowish": "other", "<script>": "other",
	} {
		if got := tireStatusClass(in); got != want {
			t.Errorf("tireStatusClass(%q) = %q, want %q", in, got, want)
		}
	}
	if TireStatusLow("Normal") || !TireStatusLow("Warning_Soft") || TireStatusLow("") || TireStatusLow("low") {
		t.Error("TireStatusLow disagrees with the classes")
	}
}

// The three copies of the low words (this package, the web's Truck card and the app's) must be the same list.
func TestTireLowWordsMatchTheClients(t *testing.T) {
	want := make([]string, 0, len(tireWords))
	for w, v := range tireWords {
		if v.class == "low" {
			want = append(want, w)
		}
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
	m.observeTires(&State{TirePressureStatusFL: "Normal", TirePressureStatusFR: "Normal", TirePressureStatusRL: "Warning_Soft", TirePressureStatusRR: "Mystery"})
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

// The one table, pinned: the values of Rivian's own app's TIRE_PRESSURE_STATUS enum plus "unknown" (what prod trucks send for no status),
// each mapped to low, not_low or unknown. Adding or changing a word is a deliberate edit of this test too.
func TestTireWordsTableIsPinned(t *testing.T) {
	want := map[string]string{
		"normal": "not_low", "warning_soft": "low", "warning_hard": "low", "warning_puncture": "low", "unspecified": "unknown", "unknown": "unknown",
	}
	if len(tireWords) != len(want) {
		t.Errorf("the table has %d words, want %d", len(tireWords), len(want))
	}
	for w, class := range want {
		if got := tireWords[w].class; got != class {
			t.Errorf("%q is %q, want %q", w, got, class)
		}
	}
	for w, v := range tireWords {
		if (v.class == "low") != (v.kind != "") {
			t.Errorf("%q: a low word has a kind and no other word does (%q, %q)", w, v.class, v.kind)
		}
	}
	// a word that is not in the table never alerts and never clears
	for _, w := range []string{"low", "very_low", "critical", "ok", "lowish", "", "tire_pressure_status_extreme"} {
		if TireStatusLow(w) || TireStatusNotLow(w) {
			t.Errorf("%q makes a claim", w)
		}
	}
}
