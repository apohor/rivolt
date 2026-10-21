package api

import (
	"strings"
	"testing"
	"time"
)

func fp(v float64) *float64 { return &v }

// An own stop (an overnight with a hookup) keeps to what that hookup allows, and
// is refused rather than clamped; a stop without a hookup keeps the old check.
func TestOwnStopBounds(t *testing.T) {
	long := strings.Repeat("x", 81)
	for _, c := range []struct {
		name string
		o    tripPlanOvernight
		want string // substring of the error, "" = accepted
	}{
		{"14-50 planned", tripPlanOvernight{Hookup: "nema_14_50", L2KW: fp(7.2)}, ""},
		{"14-50 at the charger's maximum", tripPlanOvernight{Hookup: "nema_14_50", L2KW: fp(7.6)}, ""},
		{"14-50 above the charger", tripPlanOvernight{Hookup: "nema_14_50", L2KW: fp(9.6)}, "between 1 and 7.6"},
		{"tt_30 at 1.4", tripPlanOvernight{Hookup: "tt_30", L2KW: fp(1.4)}, ""},
		{"tt_30 at 2.9 is out", tripPlanOvernight{Hookup: "tt_30", L2KW: fp(2.9)}, "between 1 and 1.4"},
		{"outlet below 1", tripPlanOvernight{Hookup: "outlet_120", L2KW: fp(0.5)}, "between 1 and 1.4"},
		{"j1772 at 11.5", tripPlanOvernight{Hookup: "j1772", L2KW: fp(11.5)}, ""},
		{"j1772 at 19.2", tripPlanOvernight{Hookup: "j1772", L2KW: fp(19.2)}, "between 1 and 11.5"},
		{"none with no power", tripPlanOvernight{Hookup: "none"}, ""},
		{"none with zero", tripPlanOvernight{Hookup: "none", L2KW: fp(0)}, ""},
		{"none with power", tripPlanOvernight{Hookup: "none", L2KW: fp(7)}, "between 0 and 0"},
		{"unknown hookup", tripPlanOvernight{Hookup: "tesla"}, "hookup must be one of"},
		{"no kW given", tripPlanOvernight{Hookup: "nema_14_50"}, ""},
		{"long name", tripPlanOvernight{Hookup: "j1772", Name: long}, "longer than 80"},
		{"markup in the name", tripPlanOvernight{Hookup: "j1772", Name: "<b>Camp</b>"}, "not allowed"},
		{"control character", tripPlanOvernight{Hookup: "j1772", Name: "Camp\x00"}, "not allowed"},
		{"ok name", tripPlanOvernight{Hookup: "j1772", Name: "Grandma's driveway"}, ""},
		// Without a hookup nothing is checked here: a hotel candidate may show a
		// 19.2 kW station rating and long names, and the plan limits the charge to
		// the truck's AC maximum.
		{"listed charger at 19.2", tripPlanOvernight{L2KW: fp(19.2), Name: long}, ""},
	} {
		got := ownStopErr(&c.o)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// With a hookup and no l2_kw the plan gets the hookup's default power, and the
// result says it was assumed.
func TestOwnStopDefaultPower(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for hookup, want := range map[string]float64{"nema_14_50": 7.2, "tt_30": 1.4, "outlet_120": 1.4, "j1772": 7.0, "none": 0} {
		req := tripPlanMultidayRequest{
			Origin: tripPoint{Lat: 34, Lon: -118},
			Days:   []tripPlanDayRequest{{End: tripPoint{Lat: 35, Lon: -118}, Overnight: &tripPlanOvernight{Hookup: hookup}}, {End: tripPoint{Lat: 36, Lon: -118}}},
		}
		if m := req.validate(now); m != "" {
			t.Fatalf("%s: validate: %s", hookup, m)
		}
		di, err := req.dayInputs()
		if err != nil {
			t.Fatal(err)
		}
		ov := di[0].Overnight
		if ov == nil || ov.L2KW == nil || *ov.L2KW != want || ov.Hookup != hookup || ov.L2KWGiven {
			t.Errorf("%s: %+v, want default %v", hookup, ov, want)
		}
	}
	// A power the user typed is kept as given.
	req := tripPlanMultidayRequest{
		Origin: tripPoint{Lat: 34, Lon: -118},
		Days:   []tripPlanDayRequest{{End: tripPoint{Lat: 35, Lon: -118}, Overnight: &tripPlanOvernight{Hookup: "nema_14_50", L2KW: fp(6.0)}}, {End: tripPoint{Lat: 36, Lon: -118}}},
	}
	di, _ := req.dayInputs()
	if ov := di[0].Overnight; *ov.L2KW != 6.0 || !ov.L2KWGiven {
		t.Errorf("typed power: %+v", ov)
	}
	// An out-of-bounds own stop is a validation error, not a clamp.
	req.Days[0].Overnight.L2KW = fp(9.6)
	if m := req.validate(now); !strings.Contains(m, "day 1") || !strings.Contains(m, "7.6") {
		t.Errorf("validate = %q", m)
	}
}

// The legacy path (no hookup) accepts what a listing can show and nothing above.
func TestLegacyOvernightPowerBound(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for kw, ok := range map[float64]bool{0: true, 7: true, 19.2: true, 19.3: false, 350: false} {
		req := tripPlanMultidayRequest{
			Origin: tripPoint{Lat: 34, Lon: -118},
			Days:   []tripPlanDayRequest{{End: tripPoint{Lat: 35, Lon: -118}, Overnight: &tripPlanOvernight{L2KW: fp(kw)}}, {End: tripPoint{Lat: 36, Lon: -118}}},
		}
		if m := req.validate(now); (m == "") != ok {
			t.Errorf("l2_kw %v: %q, accepted=%v want %v", kw, m, m == "", ok)
		}
	}
}
