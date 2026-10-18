package demo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripmultiday"
)

// samplePlansAt returns the demo's saved plans at now, priced with the
// demo's home rate and the default networks.
func samplePlansAt(t *testing.T, now time.Time) []SamplePlan {
	t.Helper()
	plans, err := SamplePlans(now, time.Time{}, settings.ChargingConfig{HomePricePerKWh: HomeRate, HomeCurrency: "USD"}, settings.DefaultChargingNetworks())
	if err != nil {
		t.Fatal(err)
	}
	return plans
}

// replayedOuting is the sample's Las Vegas trip as the ledger sees it
// after replay records it: drives (runs of D) and charges (runs of
// charging_active) from the corpus frames, placed on the wall clock so
// the trip starts at start.
func replayedOuting(t *testing.T, start time.Time) ([]ledger.Drive, []ledger.Charge) {
	t.Helper()
	c := rivian.NewSampleCorpus(rivian.SampleSeed)
	day := c.Summary().Start.Truncate(24 * time.Hour)
	frames, err := c.Frames(context.Background(), day.AddDate(0, 0, 18), day.AddDate(0, 0, 22))
	if err != nil {
		t.Fatal(err)
	}
	var shift time.Duration
	for _, f := range frames {
		if f.Gear == "D" {
			shift = start.Sub(f.At)
			break
		}
	}
	f64 := func(v float64) *float64 { return &v }
	var drives []ledger.Drive
	var charges []ledger.Charge
	for i := 0; i < len(frames); {
		f := frames[i]
		drive, charge := f.Gear == "D", f.ChargerState == "charging_active"
		if !drive && !charge {
			i++
			continue
		}
		j := i
		for j+1 < len(frames) && (frames[j+1].Gear == "D") == drive && (frames[j+1].ChargerState == "charging_active") == charge {
			j++
		}
		a, b := frames[i], frames[j]
		id := a.At.Add(shift).Format(time.RFC3339)
		if drive {
			drives = append(drives, ledger.Drive{ID: "d" + id, VehicleID: "sample", StartedAt: a.At.Add(shift), EndedAt: b.At.Add(shift),
				DistanceMi: f64((b.OdometerKm - a.OdometerKm) * 0.621371), StartLat: f64(a.Latitude), StartLon: f64(a.Longitude),
				EndLat: f64(b.Latitude), EndLon: f64(b.Longitude), StartSoC: f64(a.BatteryLevelPct), EndSoC: f64(b.BatteryLevelPct)})
		} else {
			charges = append(charges, ledger.Charge{ID: "c" + id, VehicleID: "sample", StartedAt: a.At.Add(shift), EndedAt: b.At.Add(shift),
				StartSoC: f64(a.BatteryLevelPct), EndSoC: f64(b.BatteryLevelPct), Lat: f64(a.Latitude), Lon: f64(a.Longitude),
				FinalState: "charging_complete"})
		}
		i = j + 1
	}
	return drives, charges
}

func TestSamplePlansPairWithTheReplayedTrip(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	trips := rivian.SampleTripPlans(now, time.Time{})
	plans := samplePlansAt(t, now)
	if len(plans) != 3 || len(trips) != 2 || plans[0].Name != "Las Vegas weekend" || plans[1].Name != "Las Vegas back home" {
		t.Fatalf("plans = %+v", plans)
	}
	plans = plans[:2] // the two-day trip: TestTwoDayPlanPairsEachDay
	drives, charges := replayedOuting(t, trips[0].OutingStart)
	in := ledger.Input{Drives: drives, Charges: charges, Now: now,
		Config: ledger.Config{Currency: "USD", HomeRate: HomeRate, Home: &ledger.LatLon{Lat: HomeLat, Lon: HomeLon}}}
	var outing string
	for i, sp := range plans {
		p, err := ledger.ParsePlan(sp.Inputs, sp.Plan, sp.SavedAt)
		if err != nil {
			t.Fatalf("%s: ParsePlan: %v", sp.Name, err)
		}
		cands := ledger.MatchPlan(p, in)
		if len(cands) == 0 {
			t.Fatalf("%s: no candidate", sp.Name)
		}
		c := cands[0]
		// High band: the API shows it as a suggested pairing with the
		// comparison, with no link (a write) needed.
		if c.Band != "high" || c.Signals.RouteOverlap == nil || *c.Signals.RouteOverlap != 1 {
			t.Errorf("%s: top candidate %+v", sp.Name, c)
		}
		if off := c.Signals.StartOffsetMin; off == nil || *off < 0 || *off > 15 {
			t.Errorf("%s: start offset %v min, want the drive to leave 0-15 min after the plan", sp.Name, off)
		}
		if i == 0 {
			outing = c.TripID
		} else if c.TripID != outing {
			t.Errorf("%s pairs with %s, the outbound plan with %s: want one outing", sp.Name, c.TripID, outing)
		}
		if !c.StartedAt.Equal(trips[i].OutingStart) && i == 0 {
			t.Errorf("%s: outing starts %v, want %v", sp.Name, c.StartedAt, trips[i].OutingStart)
		}
		// The plan has its own figures to compare: the stop's network
		// and price, and less energy than the drive used.
		if len(p.Stops) != 1 || p.Stops[0].Network == "" || p.Stops[0].EstCost == nil || p.Cost == nil || p.EnergyKWh == nil {
			t.Errorf("%s: plan = %+v", sp.Name, p)
		}
	}
	if plans[0].DepartAt.After(trips[0].OutingStart) || trips[0].OutingStart.Sub(plans[0].DepartAt) > 15*time.Minute {
		t.Errorf("planned departure %v, outing left %v", plans[0].DepartAt, trips[0].OutingStart)
	}
}

func TestSamplePlansInputsAreThePlannerForm(t *testing.T) {
	plans := samplePlansAt(t, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	var in struct {
		Origin      struct{ Label string }
		Destination struct{ Label string }
		ExtraStops  []any  `json:"extra_stops"`
		StartingSoC string `json:"starting_soc"`
		TargetSoC   string `json:"target_soc"`
		DepartureAt string `json:"departure_at"`
	}
	if err := json.Unmarshal(plans[0].Inputs, &in); err != nil {
		t.Fatal(err)
	}
	if in.Origin.Label != HomeLabel || in.Destination.Label != "Las Vegas, NV" || in.ExtraStops == nil ||
		in.StartingSoC == "" || in.TargetSoC != "10" {
		t.Errorf("inputs = %s", plans[0].Inputs)
	}
	// datetime-local in the truck's zone, as the planner form saves it.
	loc, _ := time.LoadLocation(planLocation)
	if dep, err := time.ParseInLocation("2006-01-02T15:04", in.DepartureAt, loc); err != nil || !dep.Equal(plans[0].DepartAt) {
		t.Errorf("departure_at %q (%v), want %v", in.DepartureAt, err, plans[0].DepartAt)
	}
}

func TestSamplePlansMoveOnOnlyWhenTheNextTripIsOver(t *testing.T) {
	if p := rivian.SampleTripPlans(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Time{}); p != nil {
		t.Fatalf("plans before any trip: %+v", p)
	}
	cur := rivian.SampleTripPlans(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), time.Time{})
	end := cur[0].OutingEnd
	if got := rivian.SampleTripPlans(end, time.Time{}); !got[0].OutingStart.Equal(cur[0].OutingStart) {
		t.Errorf("at the trip's end: plan for %v, want %v", got[0].OutingStart, cur[0].OutingStart)
	}
	if got := rivian.SampleTripPlans(end.Add(-time.Second), time.Time{}); len(got) == 2 && !got[0].OutingEnd.Before(cur[0].OutingStart) {
		t.Errorf("before the trip's end: plan for %v, want the previous loop's", got[0].OutingStart)
	}
	// Only a trip recorded whole: none when recording began after it
	// started (a fresh install's backfill), until the next one is over.
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if got := rivian.SampleTripPlans(now, cur[0].OutingStart); len(got) != 2 {
		t.Errorf("recorded from the trip's start: %d plans", len(got))
	}
	if got := rivian.SampleTripPlans(now, cur[0].OutingStart.Add(time.Second)); got != nil {
		t.Errorf("recorded from mid-trip: %+v", got)
	}
	// Stable between loops: the same plan (so no rewrite) until then.
	a := samplePlansAt(t, end.Add(time.Hour))
	b := samplePlansAt(t, end.Add(30*24*time.Hour))
	if string(a[0].Plan) != string(b[0].Plan) || string(a[1].Inputs) != string(b[1].Inputs) {
		t.Error("plans changed between loops")
	}
}

// The two-day trip is saved as the planner saves a multi-day trip, and
// Plan vs Actual pairs each day, read as the API reads it, with the
// replayed outing at high confidence: a suggestion, no link (a write).
func TestTwoDayPlanPairsEachDay(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	plans := samplePlansAt(t, now)
	two := plans[len(plans)-1]
	if two.Name != rivian.SamplePlanVegasTwoDay || two.ID != SampleTripIDs()[two.Name] {
		t.Fatalf("last plan %s %s", two.Name, two.ID)
	}
	var snap struct {
		Kind string `json:"kind"`
		Days []struct {
			Plan      map[string]json.RawMessage    `json:"plan"`
			DepartAt  time.Time                     `json:"depart_at"`
			Start     tripmultiday.Waypoint         `json:"start"`
			End       tripmultiday.Waypoint         `json:"end"`
			Costs     json.RawMessage               `json:"costs"`
			Overnight *tripmultiday.OvernightResult `json:"overnight"`
		} `json:"days"`
	}
	if err := json.Unmarshal(two.Plan, &snap); err != nil || snap.Kind != "multiday" || len(snap.Days) != 2 {
		t.Fatalf("snapshot %s: %v", two.Plan, err)
	}
	if o := snap.Days[0].Overnight; o == nil || o.L2KW != rivian.SampleHotelKW || o.PostChargeSoCPct != rivian.SampleOvernightMaxSoC ||
		!o.Capped || o.ParkedHours < 10 || snap.Days[1].Overnight != nil {
		t.Errorf("overnight %+v", o)
	}
	trips := rivian.SampleTripPlans(now, time.Time{})
	drives, charges := replayedOuting(t, trips[0].OutingStart)
	in := ledger.Input{Drives: drives, Charges: charges, Now: now,
		Config: ledger.Config{Currency: "USD", HomeRate: HomeRate, Home: &ledger.LatLon{Lat: HomeLat, Lon: HomeLon}}}
	for i, d := range snap.Days {
		dayIn, _ := json.Marshal(map[string]any{
			"origin":       map[string]any{"lat": d.Start.Latitude, "lon": d.Start.Longitude},
			"destination":  map[string]any{"lat": d.End.Latitude, "lon": d.End.Longitude},
			"departure_at": d.DepartAt.Format(time.RFC3339),
		})
		d.Plan["costs"] = d.Costs
		dayPlan, _ := json.Marshal(d.Plan)
		p, err := ledger.ParsePlan(dayIn, dayPlan, two.SavedAt)
		if err != nil {
			t.Fatalf("day %d: ParsePlan: %v", i+1, err)
		}
		cands := ledger.MatchPlan(p, in)
		if len(cands) == 0 {
			t.Fatalf("day %d: no candidate", i+1)
		}
		c := cands[0]
		if c.Band != "high" || c.Signals.RouteOverlap == nil || *c.Signals.RouteOverlap != 1 {
			t.Errorf("day %d: top candidate %+v", i+1, c)
		}
		if off := c.Signals.StartOffsetMin; off == nil || *off < 0 || *off > 15 {
			t.Errorf("day %d: start offset %v min, want the drive to leave 0-15 min after the plan", i+1, off)
		}
		if _, ok := ledger.ComparePlan(p, in, c.TripID); !ok {
			t.Errorf("day %d: no comparison", i+1)
		}
		if p.Cost == nil || len(p.Stops) != 1 {
			t.Errorf("day %d: plan = %+v", i+1, p)
		}
	}
}
