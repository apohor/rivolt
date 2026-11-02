package rivian

import (
	"regexp"
	"sort"
	"strings"
)

// What the truck itself says about each tire. A tire counts as low only when Rivian's per-wheel status says so: never
// our own pressure floor, which a cold night would trip. This is the one Go copy of the word set; the Truck card on the
// web (RIVIAN_LOW_WORDS in web/src/lib/truckHealth.ts) and in the app (rivianLowWords in ios/Rivolt/Views/TruckLogic.swift)
// carry the same list, and TestTireLowWordsMatchTheClients keeps the three together.

// tireLowWords are the words a per-wheel status uses for a low tire, after tireWord. They are the low-pressure values of the Rivian
// app's own TirePressureStatus enum (3.17.0 string table: NORMAL, WARNING_SOFT, WARNING_HARD, WARNING_PUNCTURE, UNSPECIFIED); the
// words an earlier version of this list guessed (low, very_low, critical, ...) are not in the app and were removed.
var tireLowWords = map[string]bool{"warning_soft": true, "warning_hard": true, "warning_puncture": true}

// tireNotLowWords: the one word the app's enum uses for a fine tire. Any other word, including "unspecified" and the "unknown" that
// prod sends when there is no status, makes no claim either way.
var tireNotLowWords = map[string]bool{"normal": true}

var tireSeparators = regexp.MustCompile(`[\s-]+`)

// tireWord trims, lower-cases and turns runs of spaces and hyphens into "_".
func tireWord(s string) string {
	return tireSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_")
}

// TireLowWord is the normalised low word of a per-wheel status ("" when the status is not one of the low words): the
// label vocabulary of the alert's dry-run counter.
func TireLowWord(status string) string {
	if w := tireWord(status); tireLowWords[w] {
		return w
	}
	return ""
}

// TireLowWords lists the low words, sorted: the fixed set that bounds that label.
func TireLowWords() []string {
	out := make([]string, 0, len(tireLowWords))
	for w := range tireLowWords {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
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
