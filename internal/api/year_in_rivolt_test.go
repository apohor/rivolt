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
	got := computeYear(2026, ds, cs, &yearColdest{TempF: 4, Month: "2026-01"})
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
	got := computeYear(2026, []drives.Drive{yd(10, 3, 1)}, []yearCharge{{cost: 5, currency: "USD"}, {cost: 6, currency: "USD"}}, nil)
	if got.Cost == nil || got.Cost.Estimated || got.Cost.Amount != 11 {
		t.Fatalf("%+v", got.Cost)
	}
	if got.ColdestDrive != nil {
		t.Fatal("no weather recorded means no coldest drive")
	}
	if none := computeYear(2026, nil, nil, nil); none.Cost != nil || none.LongestTrip != nil {
		t.Fatalf("empty year: %+v", none)
	}
}

// The card carries only what DA approved: no coordinates, places, ids or day-level dates, in the type or in either fixture.
func TestYearResponseCarriesOnlyTheApprovedFields(t *testing.T) {
	allowed := map[string]bool{
		"eligible": true, "year": true, "reason": true, "miles": true, "kwh": true, "charge_count": true,
		"best_efficiency_mi_per_kwh": true, "longest_trip": true, "coldest_drive": true, "cost": true,
		"month": true, "temp_f": true, "amount": true, "currency": true, "estimated": true,
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
