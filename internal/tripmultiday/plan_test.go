package tripmultiday

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// scripted plans one day per call: it arrives with the scripted SoC, takes the
// scripted drive time, and stamps the waypoint times from the input's
// departure so the orchestrator's time chaining can be checked.
type scripted struct {
	arrival []float64
	drive   []time.Duration
	dist    []float64
	fail    map[int]error // call index (0-based) -> error
	sim     bool
	calls   []rivian.PlanTripInput
}

func (f *scripted) PlanTrip(_ context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	i := len(f.calls)
	f.calls = append(f.calls, in)
	if err := f.fail[i]; err != nil {
		return nil, err
	}
	dep := in.DepartureTime
	var wps []rivian.PlannedWaypoint
	for j, w := range in.Waypoints {
		pw := rivian.PlannedWaypoint{Latitude: w.Latitude, Longitude: w.Longitude}
		switch {
		case j == 0:
			pw.WaypointType, pw.DepartureTimeUTC = "ORIGIN", dep.UTC().Format(time.RFC3339)
		case j == len(in.Waypoints)-1:
			pw.WaypointType, pw.ArrivalTimeUTC = "DESTINATION", dep.Add(f.drive[i]).UTC().Format(time.RFC3339)
		default:
			pw.WaypointType = "WAYPOINT"
			pw.ArrivalTimeUTC = dep.Add(f.drive[i] * time.Duration(j) / time.Duration(len(in.Waypoints)-1)).UTC().Format(time.RFC3339)
		}
		wps = append(wps, pw)
	}
	return &rivian.TripPlan{Status: "OK", Simulated: f.sim, Routes: []rivian.TripRoute{{
		DestinationReached: true, ArrivalSoC: f.arrival[i], TotalDriveDistanceMeters: f.dist[i],
		TotalDriveDurationSec: int(f.drive[i].Seconds()), TotalTripDurationSec: int(f.drive[i].Seconds()),
		Waypoints: wps,
	}}}, nil
}

func approx(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func f64(v float64) *float64 { return &v }

func at(h, m int) time.Time { return time.Date(2026, 10, 20, h, m, 0, 0, time.UTC) }

func wp(n float64) Waypoint { return Waypoint{Latitude: n, Longitude: n} }

func TestPlanChainsDaysByTimeAndSoC(t *testing.T) {
	// Day 1 leaves 08:00, drives 4 h, arrives 12:00 with 30 %. Overnight: 7 kW
	// until 08:00 the next day = 20 h parked: 7 * 20 * 0.9 = 126 kWh offered
	// on a 100 kWh pack, capped at the default 80 %. Day 2 then drives 3 h.
	f := &scripted{arrival: []float64{30, 25}, drive: []time.Duration{4 * time.Hour, 3 * time.Hour}, dist: []float64{400_000, 300_000}}
	resp, err := Plan(context.Background(), f, Request{
		VehicleID: "v1", PackKWh: 100, StartingSoC: 90, StartAt: at(8, 0), Origin: wp(1),
		Days: []DayInput{
			{End: wp(2), Overnight: &Overnight{Until: "08:00", Name: "Hotel"}},
			{End: wp(3)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Days) != 2 || resp.FailedDay != 0 {
		t.Fatalf("days %d, failed %d", len(resp.Days), resp.FailedDay)
	}
	d1, d2 := resp.Days[0], resp.Days[1]
	if !d1.DepartAt.Equal(at(8, 0)) || !d1.ArriveAt.Equal(at(12, 0)) {
		t.Errorf("day 1 %v -> %v", d1.DepartAt, d1.ArriveAt)
	}
	ov := d1.Overnight
	if ov == nil || !approx(ov.ParkedHours, 20, 0.01) || ov.L2KW != 7 || !ov.Capped || ov.PostChargeSoCPct != 80 {
		t.Fatalf("overnight = %+v", ov)
	}
	if !approx(ov.AddedKWh, 50, 0.01) { // 30 -> 80 on a 100 kWh pack
		t.Errorf("added = %v kWh, want 50 (what actually went in)", ov.AddedKWh)
	}
	if d2.DepartureSoC != 80 || !d2.DepartAt.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)) || !d2.ArriveAt.Equal(time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("day 2 soc %v, %v -> %v", d2.DepartureSoC, d2.DepartAt, d2.ArriveAt)
	}
	if d2.Overnight != nil {
		t.Error("the last day has no overnight")
	}
	// The planner saw each day's departure time and start.
	if !f.calls[0].DepartureTime.Equal(at(8, 0)) || !f.calls[1].DepartureTime.Equal(d2.DepartAt) || f.calls[1].StartingSoC != 80 {
		t.Errorf("planner inputs = %+v / %+v", f.calls[0], f.calls[1])
	}
	if resp.Total.DistanceMeters != 700_000 || resp.Total.DriveDurationSec != 7*3600 {
		t.Errorf("totals = %+v", resp.Total)
	}
}

func TestOvernightModel(t *testing.T) {
	cases := []struct {
		name             string
		arr, hours       float64
		l2               *float64
		cap              float64
		wantPost, wantKW float64
		wantAdded        float64
		wantCapped       bool
	}{
		// 3.3 kW * 21.4 h * 0.9 = 63.56 kWh on a 100 kWh pack: +63.56 points, under the cap and the taper (89.76 < 90).
		{"slow L2, no cap", 26.2, 21.4, f64(3.3), 100, 26.2 + 63.558, 3.3, 63.56, false},
		// The truck's AC ceiling limits a big charger: 22 kW -> 11.5.
		{"AC ceiling", 20, 4, f64(22), 100, 20 + 11.5*4*0.9/100*100, 22, 41.4, false},
		// Above 90 % the gain halves: 85 + (5 + 15/2) = 97.5, under a 100 cap.
		{"taper above 90", 85, 1, f64(50), 100, 85 + 5 + (11.5*0.9/100*100-5)/2, 50, 0, false},
		// No L2 at the stop.
		{"explicit zero", 40, 10, f64(0), 80, 40, 0, 0, false},
		// Not given: 7 kW.
		{"default L2", 20, 5, nil, 80, 20 + 7*5*0.9, 7, 31.5, false},
		// The cap stops the charge, and what went in is what fits.
		{"capped", 40, 20, f64(7), 70, 70, 7, 30, true},
		// Arriving above the cap: leaves as it came, never lower.
		{"above the cap", 85, 10, f64(7), 80, 85, 7, 0, true},
	}
	for _, c := range cases {
		res := overnight(c.arr, c.hours, &Overnight{L2KW: c.l2}, 100, 11.5, c.cap)
		if !approx(res.PostChargeSoCPct, c.wantPost, 0.02) || res.Capped != c.wantCapped || res.L2KW != c.wantKW {
			t.Errorf("%s: post %v capped %v l2 %v; want %v %v %v", c.name, res.PostChargeSoCPct, res.Capped, res.L2KW, c.wantPost, c.wantCapped, c.wantKW)
		}
		if c.wantAdded != 0 && !approx(res.AddedKWh, c.wantAdded, 1.5) {
			t.Errorf("%s: added %v, want ~%v", c.name, res.AddedKWh, c.wantAdded)
		}
	}
	// No L2 means nothing is added at all.
	if r := overnight(40, 10, &Overnight{L2KW: f64(0)}, 100, 11.5, 80); r.AddedKWh != 0 || r.PostChargeSoCPct != 40 {
		t.Errorf("no L2 = %+v", r)
	}
}

func TestNextDepartureRules(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	arrive := time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC) // 15:00 PDT
	explicit := at(5, 0)
	cases := []struct {
		name     string
		ov       *Overnight
		explicit *time.Time
		want     time.Time
	}{
		{"explicit wins", &Overnight{Until: "08:00", ParkedHours: 3}, &explicit, explicit},
		{"fixed stay", &Overnight{Until: "08:00", ParkedHours: 3}, nil, arrive.Add(3 * time.Hour)},
		{"next local 08:00", &Overnight{Until: "08:00"}, nil, time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)},
		{"same-day until later than arrival", &Overnight{Until: "18:30"}, nil, time.Date(2026, 10, 21, 1, 30, 0, 0, time.UTC)},
		{"default stay", &Overnight{}, nil, arrive.Add(10 * time.Hour)},
	}
	for _, c := range cases {
		if got := nextDeparture(arrive, c.ov, c.explicit, la); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Daylight-saving change: 08:00 local the morning after clocks go back.
	arr := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC) // Sat 15:00 PDT
	got := nextDeparture(arr, &Overnight{Until: "08:00"}, nil, la)
	if want := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC); !got.Equal(want) { // Sun 08:00 PST = 16:00Z
		t.Errorf("across the DST change: %v, want %v", got, want)
	}
}

func TestViasAreKeptAndOrdered(t *testing.T) {
	f := &scripted{arrival: []float64{40, 35}, drive: []time.Duration{3 * time.Hour, 2 * time.Hour}, dist: []float64{1, 1}}
	_, err := Plan(context.Background(), f, Request{
		PackKWh: 100, StartingSoC: 80, StartAt: at(8, 0), Origin: wp(1),
		Days: []DayInput{
			{Vias: []Waypoint{wp(10), wp(11)}, End: wp(2), Overnight: &Overnight{L2KW: f64(0)}},
			{End: wp(3)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := f.calls[0].Waypoints
	if len(w) != 4 || w[0].WaypointType != "origin" || w[1].Latitude != 10 || w[1].WaypointType != "waypoint" || w[2].Latitude != 11 || w[3].WaypointType != "destination" {
		t.Errorf("day 1 waypoints = %+v", w)
	}
	// Day 2 starts where day 1 ended.
	if w := f.calls[1].Waypoints; len(w) != 2 || w[0].Latitude != 2 || w[1].Latitude != 3 {
		t.Errorf("day 2 waypoints = %+v", w)
	}
}

func TestLimitsAreRequestErrorsNamingTheDay(t *testing.T) {
	f := &scripted{arrival: []float64{40}, drive: []time.Duration{time.Hour}, dist: []float64{1}}
	vias := func(n int) []Waypoint {
		out := make([]Waypoint, n)
		for i := range out {
			out[i] = wp(float64(i + 10))
		}
		return out
	}
	days := func(n int) []DayInput {
		out := make([]DayInput, n)
		for i := range out {
			out[i] = DayInput{End: wp(float64(i + 2))}
		}
		return out
	}
	for name, tc := range map[string]struct {
		req Request
		day int
	}{
		"day 2 has 11 waypoints": {Request{PackKWh: 100, StartingSoC: 80, Origin: wp(1), Days: []DayInput{{End: wp(2)}, {Vias: vias(9), End: wp(3)}}}, 2},
		"eleven days":            {Request{PackKWh: 100, StartingSoC: 80, Origin: wp(1), Days: days(11)}, 0},
		"no days":                {Request{PackKWh: 100, StartingSoC: 80, Origin: wp(1)}, 0},
		"no pack":                {Request{StartingSoC: 80, Origin: wp(1), Days: days(1)}, 0},
		"no soc":                 {Request{PackKWh: 100, Origin: wp(1), Days: days(1)}, 0},
		"soc over 100":           {Request{PackKWh: 100, StartingSoC: 120, Origin: wp(1), Days: days(1)}, 0},
		"bad until":              {Request{PackKWh: 100, StartingSoC: 80, Origin: wp(1), Days: []DayInput{{End: wp(2), Overnight: &Overnight{Until: "8am"}}, {End: wp(3)}}}, 1},
	} {
		_, err := Plan(context.Background(), f, tc.req)
		var re *RequestError
		if !errors.As(err, &re) || re.Day != tc.day {
			t.Errorf("%s: err = %v, want a RequestError for day %d", name, err, tc.day)
		}
	}
	if len(f.calls) != 0 {
		t.Error("a refused request must not call the planner")
	}
	// Ten days and ten waypoints a day are fine.
	g := &scripted{arrival: make([]float64, 10), drive: make([]time.Duration, 10), dist: make([]float64, 10)}
	for i := range g.arrival {
		g.arrival[i], g.drive[i] = 60, time.Hour
	}
	ds := days(10)
	ds[0].Vias = vias(8)
	for i := range ds[:9] {
		ds[i].Overnight = &Overnight{L2KW: f64(0)}
	}
	if resp, err := Plan(context.Background(), g, Request{PackKWh: 100, StartingSoC: 80, Origin: wp(1), Days: ds}); err != nil || len(resp.Days) != 10 {
		t.Errorf("10 days of up to 10 waypoints: %v", err)
	}
}

func TestPartialFailureKeepsTheDaysSoFar(t *testing.T) {
	boom := errors.New("gateway said no")
	f := &scripted{arrival: []float64{50, 0, 0}, drive: []time.Duration{time.Hour, time.Hour, time.Hour}, dist: []float64{1, 1, 1}, fail: map[int]error{1: boom}}
	resp, err := Plan(context.Background(), f, Request{
		PackKWh: 100, StartingSoC: 80, StartAt: at(8, 0), Origin: wp(1),
		Days: []DayInput{{End: wp(2), Overnight: &Overnight{L2KW: f64(0)}}, {End: wp(3), Overnight: &Overnight{}}, {End: wp(4)}},
	})
	if err != nil {
		t.Fatalf("a planner failure is not a request error: %v", err)
	}
	if resp.FailedDay != 2 || len(resp.Days) != 1 || !errors.Is(resp.FailedErr, boom) || resp.Error == "" {
		t.Fatalf("resp = %+v", resp)
	}
	if len(f.calls) != 2 {
		t.Errorf("chaining must stop at the failure, planner called %d times", len(f.calls))
	}
	if resp.Days[0].Overnight == nil {
		t.Error("the days before the failure keep their overnight")
	}
}

func TestUnreachableDayStopsTheChain(t *testing.T) {
	f := &scripted{arrival: []float64{0, 40}, drive: []time.Duration{time.Hour, time.Hour}, dist: []float64{1, 1}}
	// Make day 1 not reach its end.
	g := planFunc(func(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
		p, err := f.PlanTrip(ctx, in)
		if err == nil && len(f.calls) == 1 {
			p.Routes[0].DestinationReached = false
			p.SoCBelowLimit = true
		}
		return p, err
	})
	resp, _ := Plan(context.Background(), g, Request{
		PackKWh: 100, StartingSoC: 80, StartAt: at(8, 0), Origin: wp(1),
		Days: []DayInput{{End: wp(2), Overnight: &Overnight{}}, {End: wp(3)}},
	})
	if len(resp.Days) != 1 || resp.Days[0].DestinationReached || !resp.Days[0].SoCBelowLimit || resp.FailedDay != 2 || len(f.calls) != 1 {
		t.Errorf("resp = %+v (planner calls %d)", resp, len(f.calls))
	}
}

type planFunc func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error)

func (p planFunc) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	return p(ctx, in)
}

func TestExplicitDepartureBeforeArrivalIsRefused(t *testing.T) {
	f := &scripted{arrival: []float64{50, 40}, drive: []time.Duration{4 * time.Hour, time.Hour}, dist: []float64{1, 1}}
	early := at(9, 0) // day 1 arrives at 12:00
	resp, err := Plan(context.Background(), f, Request{
		PackKWh: 100, StartingSoC: 80, StartAt: at(8, 0), Origin: wp(1),
		Days: []DayInput{{End: wp(2), Overnight: &Overnight{}}, {End: wp(3), DepartAt: &early}},
	})
	if err != nil || resp.FailedDay != 2 || len(f.calls) != 1 || len(resp.Days) != 1 {
		t.Errorf("resp = %+v err %v calls %d", resp, err, len(f.calls))
	}
}

func TestSimulatedAndTargetsAndFallbacks(t *testing.T) {
	f := &scripted{arrival: []float64{40, 35}, drive: []time.Duration{time.Hour, time.Hour}, dist: []float64{1, 1}, sim: true}
	dayTarget := 25.0
	reqTarget := 15.0
	resp, _ := Plan(context.Background(), f, Request{
		PackKWh: 100, StartingSoC: 80, StartAt: at(8, 0), Origin: wp(1), TargetArrivalSoCPct: &reqTarget,
		Days: []DayInput{{End: wp(2), TargetArrivalSoCPct: &dayTarget, Overnight: &Overnight{L2KW: f64(0)}}, {End: wp(3)}},
	})
	if !resp.Simulated {
		t.Error("any simulated day makes the response simulated")
	}
	if *f.calls[0].TargetArrivalSocPercent != 25 || *f.calls[1].TargetArrivalSocPercent != 15 {
		t.Errorf("targets = %v / %v, want the day's own, then the request's", *f.calls[0].TargetArrivalSocPercent, *f.calls[1].TargetArrivalSocPercent)
	}
	// With no clock input the first day leaves at Now to the minute.
	g := &scripted{arrival: []float64{40}, drive: []time.Duration{time.Hour}, dist: []float64{1}}
	_, _ = Plan(context.Background(), g, Request{PackKWh: 100, StartingSoC: 80, Origin: wp(1), Days: []DayInput{{End: wp(2)}},
		Now: func() time.Time { return time.Date(2026, 12, 1, 9, 30, 45, 0, time.UTC) }})
	if !g.calls[0].DepartureTime.Equal(time.Date(2026, 12, 1, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("default start = %v", g.calls[0].DepartureTime)
	}
	// Arrival falls back to the route's duration when the waypoints carry no time.
	h := planFunc(func(_ context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
		return &rivian.TripPlan{Routes: []rivian.TripRoute{{DestinationReached: true, ArrivalSoC: 50, TotalTripDurationSec: 7200}}}, nil
	})
	r, _ := Plan(context.Background(), h, Request{PackKWh: 100, StartingSoC: 80, StartAt: at(8, 0), Origin: wp(1), Days: []DayInput{{End: wp(2)}}})
	if !r.Days[0].ArriveAt.Equal(at(10, 0)) {
		t.Errorf("fallback arrival = %v", r.Days[0].ArriveAt)
	}
}
