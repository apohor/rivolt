// Package tripprofile derives an energy-consumption multiplier from
// a user's editable VehicleProfile (wheel size, tire type, roof /
// rear accessories, default cargo). The trip planner doesn't get
// these inputs through to Rivian's gateway, but they materially
// affect highway energy use; this multiplier is the post-correction
// the planner layers on top of Rivian's plan response, alongside
// the weather correction in internal/tripweather.
//
// Coefficients are conservative mid-band defensible values from
// owner reports + spot-check range tests on the R1 platform
// (InsideEVs, Out of Spec). They're meant to be honest about
// "your config will burn more energy than the factory baseline"
// without claiming precision we can't validate. The long-term fix
// is fitting these against the user's own recorded drives - that's
// deferred (slice C of the rate-table plan, equivalently here).
package tripprofile

import (
	"strings"

	"github.com/apohor/rivolt/internal/db"
)

// Multiplier returns the trip-wide energy multiplier for the given
// profile. 1.0 = factory baseline (21" wheels, all-season tires, no
// accessories, no extra cargo). Higher = burns more energy.
//
// Stacked floor 0.85 / ceiling 1.5 keeps pathological combinations
// from producing fantasy numbers either direction. The cap exists
// because real-world energy hit from extreme configurations (heavy
// load + AT tires + rooftop tent) is non-linear and the simple
// product over-estimates past ~50%.
func Multiplier(p db.VehicleProfile) float64 {
	m := wheelFactor(p.WheelInches)
	m *= tireFactor(p.TireType)
	m *= accessoryFactor(p.Accessories)
	m *= loadFactor(p.DefaultExtraLoadLb)
	if m < 0.85 {
		m = 0.85
	}
	if m > 1.5 {
		m = 1.5
	}
	return m
}

// Reason describes one user-visible contributor to the multiplier.
// The trip-planner chip joins these into the "reasons" list so the
// user can see why the planner thinks their arrival SoC is lower
// than Rivian's number. Empty Label means "no contribution" -
// callers should drop those entries.
type Reason struct {
	Label  string
	Factor float64
}

// Reasons returns the list of contributors that pushed the multiplier
// away from 1.0. Used by the chip's reason builder.
func Reasons(p db.VehicleProfile) []Reason {
	out := make([]Reason, 0, 4)
	if f := wheelFactor(p.WheelInches); f != 1.0 && p.WheelInches > 0 {
		out = append(out, Reason{Label: wheelLabel(p.WheelInches), Factor: f})
	}
	if f := tireFactor(p.TireType); f != 1.0 {
		out = append(out, Reason{Label: tireLabel(p.TireType), Factor: f})
	}
	if f := accessoryFactor(p.Accessories); f != 1.0 {
		out = append(out, Reason{Label: accessoryLabel(p.Accessories), Factor: f})
	}
	if f := loadFactor(p.DefaultExtraLoadLb); f != 1.0 {
		out = append(out, Reason{Label: loadLabel(p.DefaultExtraLoadLb), Factor: f})
	}
	return out
}

// Wheel factor anchors on R1's stock 21". Smaller wheels (offered
// as a winter / max-range option) reduce drag and rolling losses;
// larger wheels (22" upgrade) increase both.
func wheelFactor(inches int) float64 {
	switch inches {
	case 18, 19:
		return 0.97
	case 20:
		return 0.98
	case 21:
		return 1.00
	case 22:
		return 1.04
	}
	return 1.00
}

func wheelLabel(inches int) string {
	switch inches {
	case 18, 19, 20:
		return formatInches(inches) + " wheels (lower drag)"
	case 22:
		return "22-inch wheels"
	}
	return ""
}

// Tire factor: all-terrain has a chunky tread that raises rolling
// resistance materially; winter tires use a soft compound; summer
// tires are low-rolling-resistance and slightly help.
func tireFactor(t string) float64 {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "all_terrain":
		return 1.06
	case "winter":
		return 1.05
	case "summer":
		return 0.99
	}
	return 1.00
}

func tireLabel(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "all_terrain":
		return "AT tires"
	case "winter":
		return "winter tires"
	case "summer":
		return "summer tires (low rolling resistance)"
	}
	return ""
}

// Accessory factor: roof items dominate (frontal area drag at
// highway speed); rear racks are smaller. A rooftop tent is so
// much bigger than other roof items that we don't stack a rack on
// top of it - the tent already implies the rack's drag.
func accessoryFactor(accessories []string) float64 {
	has := func(s string) bool {
		for _, a := range accessories {
			if strings.EqualFold(strings.TrimSpace(a), s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("rooftop_tent"):
		return 1.12
	case has("cargo_box"), has("ski_box"):
		f := 1.06
		if has("bike_rack") {
			f *= 1.02
		}
		return f
	case has("bike_rack"):
		return 1.04
	case has("roof_rack"):
		return 1.03
	}
	return 1.00
}

func accessoryLabel(accessories []string) string {
	has := func(s string) bool {
		for _, a := range accessories {
			if strings.EqualFold(strings.TrimSpace(a), s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("rooftop_tent"):
		return "rooftop tent"
	case has("cargo_box"):
		return "cargo box"
	case has("ski_box"):
		return "ski box"
	case has("bike_rack"):
		return "bike rack"
	case has("roof_rack"):
		return "roof rack"
	}
	return ""
}

// Load factor: ~0.5% per 100 lb at highway speeds, capped at 5%
// (anything above 1000 lb is a payload class change we don't try
// to model). The 0.5%/100 lb number is empirical; on an R1 the
// extra mass mostly shows up as rolling-resistance overhead.
func loadFactor(extraLb float64) float64 {
	if extraLb <= 0 {
		return 1.0
	}
	inc := 0.005 * extraLb / 100
	if inc > 0.05 {
		inc = 0.05
	}
	return 1.0 + inc
}

func loadLabel(extraLb float64) string {
	if extraLb <= 0 {
		return ""
	}
	return formatLb(extraLb) + " cargo"
}

func formatInches(in int) string {
	switch in {
	case 18:
		return "18-inch"
	case 19:
		return "19-inch"
	case 20:
		return "20-inch"
	case 21:
		return "21-inch"
	case 22:
		return "22-inch"
	}
	return "stock"
}

func formatLb(lb float64) string {
	if lb < 1000 {
		return roundTo50(lb) + " lb"
	}
	return roundTo50(lb) + " lb"
}

func roundTo50(v float64) string {
	rounded := int((v+25)/50) * 50
	return itoa(rounded)
}

// itoa avoids the stdlib strconv dep for one call site; keeps the
// package's import surface narrow.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
