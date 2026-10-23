package rivian

import (
	"regexp"
	"strings"
)

// What the truck itself says about each tire. A tire counts as low only when Rivian's per-wheel status says so: never
// our own pressure floor, which a cold night would trip. This is the one Go copy of the word set; the Truck card on the
// web (RIVIAN_LOW_WORDS in web/src/lib/truckHealth.ts) and in the app (rivianLowWords in ios/Rivolt/Views/TruckLogic.swift)
// carry the same list, and TestTireLowWordsMatchTheClients keeps the three together.

// tireLowWords are the words a per-wheel status uses for a low tire, after tireWord.
var tireLowWords = map[string]bool{
	"low": true, "low_pressure": true, "very_low": true, "critically_low": true, "critical_low": true, "critical": true,
}

// tireNotLowWords are the words seen to say a tire is fine. Any other word makes no claim either way.
var tireNotLowWords = map[string]bool{"normal": true, "ok": true}

var tireSeparators = regexp.MustCompile(`[\s-]+`)

// tireWord trims, lower-cases and turns runs of spaces and hyphens into "_".
func tireWord(s string) string {
	return tireSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_")
}

// TireStatusLow reports whether Rivian's own per-wheel status is one of its low words.
func TireStatusLow(status string) bool { return tireLowWords[tireWord(status)] }

// TireStatusNotLow reports whether Rivian's own per-wheel status is one of the words seen to say the tire is fine.
func TireStatusNotLow(status string) bool { return tireNotLowWords[tireWord(status)] }

// Tire status classes, the whole label vocabulary of rivolt_tire_status_total.
const (
	tireClassLow     = "low"
	tireClassNotLow  = "not_low"
	tireClassOther   = "other"
	tireClassMissing = "missing"
)

// tireStatusClass buckets one wheel's status for the counter: low, not_low, other (a word we have no claim for) or
// missing (no status at all).
func tireStatusClass(status string) string {
	w := tireWord(status)
	switch {
	case w == "":
		return tireClassMissing
	case tireLowWords[w]:
		return tireClassLow
	case tireNotLowWords[w]:
		return tireClassNotLow
	}
	return tireClassOther
}
