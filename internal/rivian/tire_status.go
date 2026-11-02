package rivian

import (
	"regexp"
	"strings"
)

// What the truck itself says about each tire. A tire counts as low only when Rivian's own per-wheel status says so: never
// our own pressure floor, which a cold night would trip.
//
// tireWords is the ONE table of status words (DA's ruling, Nov 2): the values of the TIRE_PRESSURE_STATUS enum in Rivian's own
// app, without its prefix, plus "unknown", the word prod trucks send for a wheel they have no status for. Every word maps to
// low, not_low or unknown, and a word that is not in the table makes no claim at all: it never alerts, never clears, is counted
// as "other" and logged once per process. The web Truck card (RIVIAN_LOW_WORDS in web/src/lib/truckHealth.ts) and the app
// (rivianLowWords in ios/Rivolt/Views/TruckLogic.swift) carry the low words, and TestTireLowWordsMatchTheClients keeps the
// three together.
//
// kind is only for the alert's dry-run counter (rivolt_tire_low_would_send_total{class}): which low word it was.
var tireWords = map[string]struct{ class, kind string }{
	"normal":           {"not_low", ""},
	"warning_soft":     {"low", "soft"},
	"warning_hard":     {"low", "hard"},
	"warning_puncture": {"low", "puncture"},
	"unspecified":      {"unknown", ""},
	"unknown":          {"unknown", ""},
}

var tireSeparators = regexp.MustCompile(`[\s-]+`)

// tireWord trims, lower-cases and turns runs of spaces and hyphens into "_". (The enum's own TIRE_PRESSURE_STATUS_ prefix is not stripped:
// the web and the app do not, and the three must agree.)
func tireWord(s string) string {
	return tireSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_")
}

// TireStatusLow reports whether Rivian's own per-wheel status is one of its low words.
func TireStatusLow(status string) bool { return tireWords[tireWord(status)].class == "low" }

// TireStatusNotLow reports whether Rivian's own per-wheel status is the word that says the tire is fine.
func TireStatusNotLow(status string) bool { return tireWords[tireWord(status)].class == "not_low" }

// TireLowKind is which low word a status is (soft, hard or puncture), "" for any other word.
func TireLowKind(status string) string { return tireWords[tireWord(status)].kind }

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
	case tireWords[w].class == "low":
		return tireClassLow
	case tireWords[w].class == "not_low":
		return tireClassNotLow
	}
	return tireClassOther // "unknown"/"unspecified" and any word not in the table: no claim either way
}
