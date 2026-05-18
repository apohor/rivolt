package tripmultiday

import (
	"context"
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
)

// fakePlanner returns scripted arrival SoCs so the orchestrator's
// daisy-chain logic can be tested without a Rivian client.
type fakePlanner struct {
	// arrivalPct[i] is the arrival SoC the i-th PlanTrip call returns.
	arrivalPct []float64
	distance   []float64
	calls      []rivian.PlanTripInput
}

func (f *fakePlanner) PlanTrip(_ context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	i := len(f.calls)
	f.calls = append(f.calls, in)
	plan := &rivian.TripPlan{
		Routes: []rivian.TripRoute{{
			DestinationReached:       true,
			ArrivalSoC:               f.arrivalPct[i],
			TotalDriveDistanceMeters: f.distance[i],
		}},
	}
	return plan, nil
}

func approx(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestPlan_TwoDayChain_L2OvernightAddsSoC(t *testing.T) {
	// Day 1: start 80%, drive to hotel, arrive 25%.
	// Overnight: 7 kW × 10 h = 70 kWh on a 100 kWh pack = +70 pp →
	//            25 + 70 = 95, capped at 80 (default).
	// Day 2: start 80%, drive to dest, arrive 30%.
	f := &fakePlanner{
		arrivalPct: []float64{25, 30},
		distance:   []float64{400_000, 350_000},
	}
	resp, err := Plan(context.Background(), f, Request{
		VehicleID:   "v1",
		PackKWh:     100,
		StartingSoC: 80,
		Origin:      Waypoint{Latitude: 1, Longitude: 1},
		Overnights: []OvernightStop{{
			Waypoint: Waypoint{Latitude: 2, Longitude: 2},
			Name:     "Hotel A",
			L2KW:     7,
		}},
		Destination: Waypoint{Latitude: 3, Longitude: 3},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, want := len(resp.Days), 2; got != want {
		t.Fatalf("days: got %d want %d", got, want)
	}
	d1, d2 := resp.Days[0], resp.Days[1]
	if d1.DepartureSoC != 80 || d1.ArrivalSoC != 25 {
		t.Errorf("day 1 SoC: dep=%v arr=%v", d1.DepartureSoC, d1.ArrivalSoC)
	}
	if d1.Overnight == nil {
		t.Fatal("day 1 overnight missing")
	}
	if !d1.Overnight.Capped {
		t.Errorf("day 1 overnight should be capped (25 + 70 > 80)")
	}
	if !approx(d1.Overnight.PostChargeSoCPct, 80, 0.001) {
		t.Errorf("day 1 post-charge: got %v want 80", d1.Overnight.PostChargeSoCPct)
	}
	if !approx(d2.DepartureSoC, 80, 0.001) {
		t.Errorf("day 2 departure: got %v want 80 (capped from overnight)", d2.DepartureSoC)
	}
	if d2.Overnight != nil {
		t.Error("destination day should have no overnight")
	}
	if !approx(resp.Total.DistanceMeters, 750_000, 1) {
		t.Errorf("total distance: got %v want 750000", resp.Total.DistanceMeters)
	}
}

func TestPlan_NoL2OvernightCarriesSoC(t *testing.T) {
	// Day 1: 80 → 35 (arrived at friend's house, no L2).
	// Day 2 starts at 35 (unchanged).
	f := &fakePlanner{arrivalPct: []float64{35, 40}, distance: []float64{200_000, 180_000}}
	resp, err := Plan(context.Background(), f, Request{
		VehicleID:   "v1",
		PackKWh:     100,
		StartingSoC: 80,
		Origin:      Waypoint{Latitude: 1, Longitude: 1},
		Overnights: []OvernightStop{{
			Waypoint: Waypoint{Latitude: 2, Longitude: 2},
			Name:     "Friend's house",
			L2KW:     0, // explicit: no charging
		}},
		Destination: Waypoint{Latitude: 3, Longitude: 3},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	d1, d2 := resp.Days[0], resp.Days[1]
	if d1.Overnight == nil {
		t.Fatal("overnight metadata missing even when no L2")
	}
	if d1.Overnight.Capped {
		t.Error("no-L2 overnight should not be marked capped")
	}
	if !approx(d2.DepartureSoC, 35, 0.001) {
		t.Errorf("day 2 departure: got %v want 35", d2.DepartureSoC)
	}
}

func TestPlan_PartialL2UnderCap(t *testing.T) {
	// 4 kW × 8 h = 32 kWh on a 100 kWh pack = +32 pp, 30 + 32 = 62%,
	// below 80% cap → not capped.
	f := &fakePlanner{arrivalPct: []float64{30, 50}, distance: []float64{300_000, 280_000}}
	resp, err := Plan(context.Background(), f, Request{
		VehicleID:   "v1",
		PackKWh:     100,
		StartingSoC: 90,
		Origin:      Waypoint{Latitude: 1, Longitude: 1},
		Overnights: []OvernightStop{{
			Waypoint:    Waypoint{Latitude: 2, Longitude: 2},
			L2KW:        4,
			ParkedHours: 8,
		}},
		Destination: Waypoint{Latitude: 3, Longitude: 3},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ov := resp.Days[0].Overnight
	if ov.Capped {
		t.Error("should not be capped (62 < 80)")
	}
	if !approx(ov.PostChargeSoCPct, 62, 0.001) {
		t.Errorf("post-charge: got %v want 62", ov.PostChargeSoCPct)
	}
	if !approx(ov.AddedKWh, 32, 0.001) {
		t.Errorf("added kWh: got %v want 32", ov.AddedKWh)
	}
	if !approx(resp.Days[1].DepartureSoC, 62, 0.001) {
		t.Errorf("day 2 dep: got %v want 62", resp.Days[1].DepartureSoC)
	}
}

func TestPlan_LegSequencingFromTo(t *testing.T) {
	// Three days: origin → ov1 → ov2 → destination. Verify each leg's
	// waypoints are correct and StartingSoC daisies through.
	f := &fakePlanner{
		arrivalPct: []float64{20, 22, 28},
		distance:   []float64{300_000, 320_000, 280_000},
	}
	resp, err := Plan(context.Background(), f, Request{
		VehicleID:   "v1",
		PackKWh:     100,
		StartingSoC: 85,
		Origin:      Waypoint{Latitude: 10, Longitude: 100},
		Overnights: []OvernightStop{
			{Waypoint: Waypoint{Latitude: 20, Longitude: 200}, L2KW: 7, ParkedHours: 10}, // +70 → 90 → 80 capped
			{Waypoint: Waypoint{Latitude: 30, Longitude: 300}, L2KW: 0},                 // no charge → carry
		},
		Destination: Waypoint{Latitude: 40, Longitude: 400},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := len(f.calls); got != 3 {
		t.Fatalf("PlanTrip calls: got %d want 3", got)
	}
	// Leg 1: origin → ov1, starting 85
	if f.calls[0].Waypoints[0].Latitude != 10 || f.calls[0].Waypoints[1].Latitude != 20 {
		t.Errorf("leg 1 endpoints wrong: %+v", f.calls[0].Waypoints)
	}
	if f.calls[0].StartingSoC != 85 {
		t.Errorf("leg 1 starting SoC: got %v want 85", f.calls[0].StartingSoC)
	}
	// Leg 2: ov1 → ov2, starting 80 (capped from L2)
	if f.calls[1].Waypoints[0].Latitude != 20 || f.calls[1].Waypoints[1].Latitude != 30 {
		t.Errorf("leg 2 endpoints wrong: %+v", f.calls[1].Waypoints)
	}
	if !approx(f.calls[1].StartingSoC, 80, 0.001) {
		t.Errorf("leg 2 starting SoC: got %v want 80 (capped overnight)", f.calls[1].StartingSoC)
	}
	// Leg 3: ov2 → destination, starting = day-2 arrival (22), no L2
	if f.calls[2].Waypoints[0].Latitude != 30 || f.calls[2].Waypoints[1].Latitude != 40 {
		t.Errorf("leg 3 endpoints wrong: %+v", f.calls[2].Waypoints)
	}
	if !approx(f.calls[2].StartingSoC, 22, 0.001) {
		t.Errorf("leg 3 starting SoC: got %v want 22 (no-L2 carry)", f.calls[2].StartingSoC)
	}
	if resp.Days[2].Overnight != nil {
		t.Error("destination day should not carry overnight")
	}
}

func TestPlan_RejectsZeroPackOrSoC(t *testing.T) {
	f := &fakePlanner{}
	_, err := Plan(context.Background(), f, Request{VehicleID: "v1", PackKWh: 0, StartingSoC: 80})
	if err == nil {
		t.Error("expected error on zero pack_kwh")
	}
	_, err = Plan(context.Background(), f, Request{VehicleID: "v1", PackKWh: 100, StartingSoC: 0})
	if err == nil {
		t.Error("expected error on zero starting_soc")
	}
}
