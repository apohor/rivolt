package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/drives"
)

func yd(mi, kwh float64, month time.Month) drives.Drive {
	return drives.Drive{DistanceMi: mi, EnergyUsedKWh: kwh, StartedAt: time.Date(2026, month, 5, 9, 0, 0, 0, time.UTC)}
}

func TestComputeYearFigures(t *testing.T) {
	ds := []drives.Drive{
		yd(10, 5, time.January),
		yd(120, 40, time.July), // 3.0 mi/kWh and the longest
		yd(2, 0.1, time.March), // 20 mi/kWh but under the efficiency floor
		yd(30, 9, time.May),    // 3.33
	}
	cs := []yearCharge{
		{cost: 10, currency: "USD"},
		{estimated: 4, currency: "USD"},
		{cost: 7, currency: "EUR"}, // the smaller spend: left out of a one-currency card
	}
	got := computeYear(2026, ds, cs, yearExtras{coldest: &yearColdest{TempF: 4, Month: "2026-01"}})
	if got.Miles != 162 || got.KWh != 54.1 {
		t.Fatalf("totals: %+v", got)
	}
	if got.ChargeCount != 3 {
		t.Fatalf("charge count counts every charge: %d", got.ChargeCount)
	}
	if got.BestMiPerKWh < 3.33 || got.BestMiPerKWh > 3.34 {
		t.Fatalf("best efficiency ignores drives under %v mi: %v", yearMinEfficiencyMi, got.BestMiPerKWh)
	}
	if got.LongestTrip == nil || got.LongestTrip.Miles != 120 || got.LongestTrip.Month != "2026-07" {
		t.Fatalf("longest trip: %+v", got.LongestTrip)
	}
	if got.Cost == nil || got.Cost.Currency != "USD" || got.Cost.Amount != 14 || !got.Cost.Estimated {
		t.Fatalf("cost: %+v", got.Cost)
	}
	if got.ColdestDrive == nil {
		t.Fatal("coldest drive passed through")
	}
}

func TestComputeYearCostMeasuredIsNotEstimated(t *testing.T) {
	got := computeYear(2026, []drives.Drive{yd(10, 3, 1)}, []yearCharge{{cost: 5, currency: "USD"}, {cost: 6, currency: "USD"}}, yearExtras{})
	if got.Cost == nil || got.Cost.Estimated || got.Cost.Amount != 11 {
		t.Fatalf("%+v", got.Cost)
	}
	if got.ColdestDrive != nil {
		t.Fatal("no weather recorded means no coldest drive")
	}
	if none := computeYear(2026, nil, nil, yearExtras{}); none.Cost != nil || none.LongestTrip != nil {
		t.Fatalf("empty year: %+v", none)
	}
}

// The card carries only what DA approved: no coordinates, places, ids or day-level dates, in the type or in either fixture.
func TestYearResponseCarriesOnlyTheApprovedFields(t *testing.T) {
	allowed := map[string]bool{
		"eligible": true, "year": true, "reason": true, "miles": true, "kwh": true, "charge_count": true,
		"best_efficiency_mi_per_kwh": true, "longest_trip": true, "coldest_drive": true, "cost": true,
		"month": true, "temp_f": true, "amount": true, "currency": true, "estimated": true,
		"since_month": true, "drive_count": true, "energy_shares": true, "home": true, "away": true, "fast": true,
		"efficiency_mi_per_kwh": true, "efficiency_estimated": true, "most_efficient_long_drive": true, "mi_per_kwh": true,
		"planned_trips": true, "planned_stops": true, "model": true, "cost_per_mile": true,
	}
	ty := map[string]bool{}
	typeKeys(reflect.TypeOf(yearResponse{}), ty, map[reflect.Type]bool{})
	for _, name := range []string{"year_in_rivolt.json", "year_in_rivolt_not_yet.json"} {
		fx := map[string]bool{}
		jsonKeys(readFixture(t, name), fx)
		for k := range fx {
			if !allowed[k] {
				t.Errorf("%s carries %q, which is not on the approved field list", name, k)
			}
		}
	}
	for k := range ty {
		if !allowed[k] {
			t.Errorf("yearResponse can carry %q, which is not on the approved field list", k)
		}
	}
	b, _ := json.Marshal(yearResponse{Year: 2026, Reason: "account_too_new"})
	if strings.Contains(string(b), "miles") || strings.Contains(string(b), "cost") {
		t.Fatalf("a refused card carries no figures: %s", b)
	}
}

func TestYearGate(t *testing.T) {
	now := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	old, young := now.Add(-yearMinAccountAge-time.Hour), now.Add(-yearMinAccountAge+time.Hour)
	for _, c := range []struct {
		name            string
		created         time.Time
		drives, charges int
		want            string
	}{
		{"enough", old, 10, 3, ""},
		{"nine drives", old, 9, 3, "not_enough_activity"},
		{"two charges", old, 10, 2, "not_enough_activity"},
		{"under 30 days wins over activity", young, 500, 50, "account_too_new"},
	} {
		if got := yearGate(now, c.created, c.drives, c.charges); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestComputeYearCardFigures(t *testing.T) {
	ds := []drives.Drive{
		yd(10, 4, time.January),   // 2.5
		yd(60, 15, time.April),    // 4.0: long
		yd(80, 16, time.August),   // 5.0: long, the most efficient
		yd(40, 0, time.September), // no energy: left out of the efficiency
	}
	cs := []yearCharge{
		{kwh: 30, where: "home", cost: 6, currency: "USD"},
		{kwh: 10, where: "away", cost: 4, currency: "USD"},
		{kwh: 60, where: "fast", cost: 30, currency: "USD"},
		{kwh: 50, where: "", cost: 0, currency: "USD"}, // could not be placed: not in the shares
	}
	got := computeYear(2026, ds, cs, yearExtras{plannedTrips: 2, plannedStops: 5, model: "R1S", sinceMonth: "2026-03"})
	if got.DriveCount != 4 || got.PlannedTrips == nil || *got.PlannedTrips != 2 || *got.PlannedStops != 5 || got.Model != "R1S" || got.SinceMonth != "2026-03" {
		t.Fatalf("%+v", got)
	}
	if want := 150.0 / 35.0; got.EfficiencyMiPerKWh < want-1e-9 || got.EfficiencyMiPerKWh > want+1e-9 || !got.EfficiencyEstimated {
		t.Fatalf("efficiency %v %v, want %v", got.EfficiencyMiPerKWh, got.EfficiencyEstimated, want)
	}
	if l := got.MostEfficientLong; l == nil || l.Miles != 80 || l.MiPerKWh != 5 || l.Month != "2026-08" {
		t.Fatalf("most efficient long drive: %+v", l)
	}
	if sh := got.EnergyShares; sh == nil || sh.Home != 0.3 || sh.Away != 0.1 || sh.Fast != 0.6 {
		t.Fatalf("shares: %+v", sh)
	}
	if got.Cost == nil || got.Cost.Amount != 40 || got.CostPerMile != 40.0/190.0 {
		t.Fatalf("cost %+v per mile %v", got.Cost, got.CostPerMile)
	}
	// Nothing priced: no cost and no cost per mile. No drive of 50 mi: no long drive. No placed charge: no shares.
	bare := computeYear(2026, []drives.Drive{yd(10, 4, 1)}, []yearCharge{{kwh: 5}}, yearExtras{})
	if bare.Cost != nil || bare.CostPerMile != 0 || bare.MostEfficientLong != nil || bare.EnergyShares != nil || bare.Model != "" {
		t.Fatalf("%+v", bare)
	}
	if bare.PlannedTrips == nil || *bare.PlannedTrips != 0 {
		t.Fatal("zero planned trips is still a figure")
	}
}

func TestPlannedStopsAndModel(t *testing.T) {
	plan := `{"Routes":[{"Waypoints":[{"WaypointType":"ORIGIN"},{"WaypointType":"DC_CHARGE_STATION"},{"WaypointType":"DC_CHARGE_STATION"},{"WaypointType":"DESTINATION"}]},{"Waypoints":[{"WaypointType":"DC_CHARGE_STATION"}]}]}`
	if n := plannedStops([]byte(plan)); n != 2 {
		t.Errorf("stops of the first route: %d", n)
	}
	for _, bad := range []string{"", "null", "{", `{"Routes":[]}`} {
		if n := plannedStops([]byte(bad)); n != 0 {
			t.Errorf("%q: %d", bad, n)
		}
	}
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"R1S"}, "R1S"}, {[]string{"r1s", "R1S"}, "R1S"}, {[]string{"R1S", "R2"}, ""},
		{[]string{"R1S", ""}, ""}, {nil, ""}, {[]string{"Ryan's truck"}, ""},
	} {
		if got := commonModel(c.in); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}
