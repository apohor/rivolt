package syntheticplan

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
)

// A straight route along a meridian: one degree of latitude is ~111.2 km.
// At 2.3 mi/kWh and a 131 kWh pack, one metre costs 1/(2.3*1609.344)/131*100
// = 0.0002062 SoC points, so 100 km is ~20.6 points.
type fakeRouter struct {
	lat0     float64
	legKm    []float64
	speedKmh float64
	err      error
	calls    int
}

func (r *fakeRouter) Route(_ context.Context, locs [][2]float64, _ maps.RouteOpts) (maps.Route, error) {
	r.calls++
	if r.err != nil {
		return maps.Route{}, r.err
	}
	var out maps.Route
	lat := r.lat0
	for i := range locs[1:] {
		km := r.legKm[i]
		end := lat + km/111.195
		var shape [][2]float64
		const steps = 50
		for s := 0; s <= steps; s++ {
			shape = append(shape, [2]float64{lat + (end-lat)*float64(s)/steps, -118})
		}
		out.Legs = append(out.Legs, maps.RouteLeg{LengthMeters: km * 1000, TimeSec: km / r.speedKmh * 3600, Shape: shape})
		lat = end
	}
	return out, nil
}

type fakeChargers struct {
	pois []chargers.POI
	err  error
}

func (f fakeChargers) QueryCorridor([][2]float64, chargers.Filter, chargers.QueryCorridorOptions) ([]chargers.POI, error) {
	return f.pois, f.err
}

// stationAt puts a charger on the route km kilometres from the origin.
func stationAt(lat0, km, kw float64, name, network string) chargers.POI {
	return chargers.POI{Lat: lat0 + km/111.195, Lon: -118, Name: name, EVNetwork: network, IsDCFC: true, MaxPowerKW: kw, EntityID: name}
}

var pinned = time.Date(2026, 12, 1, 8, 0, 20, 0, time.UTC)

func newPlanner(r Router, c Chargers) *Planner {
	return &Planner{Router: r, Chargers: c, Vehicle: Vehicle{PackKWh: 131, MiPerKWh: 2.3, DCMaxKW: 220}, Now: func() time.Time { return pinned }}
}

func input(soc float64, n int) rivian.PlanTripInput {
	in := rivian.PlanTripInput{StartingSoC: soc}
	for i := 0; i < n; i++ {
		t := "waypoint"
		if i == 0 {
			t = "origin"
		} else if i == n-1 {
			t = "destination"
		}
		in.Waypoints = append(in.Waypoints, rivian.PlanTripWaypoint{Latitude: 34 + float64(i), Longitude: -118, WaypointType: t})
	}
	return in
}

func stopsOf(p *rivian.TripPlan) []rivian.PlannedWaypoint {
	var out []rivian.PlannedWaypoint
	for _, w := range p.Routes[0].Waypoints {
		if w.WaypointType == "DC_CHARGE_STATION" {
			out = append(out, w)
		}
	}
	return out
}

func TestShortTripNeedsNoCharge(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{100}, speedKmh: 100}
	got, err := newPlanner(r, fakeChargers{}).PlanTrip(context.Background(), input(80, 2))
	if err != nil {
		t.Fatal(err)
	}
	rt := got.Routes[0]
	if !got.Simulated || got.Status != "OK" || !rt.DestinationReached || len(stopsOf(got)) != 0 {
		t.Fatalf("plan = %+v", got)
	}
	// 100 km = 20.6 points, so 80 - 20.6 = 59.4 at arrival.
	if math.Abs(rt.ArrivalSoC-59.4) > 0.2 {
		t.Errorf("arrival SoC = %v, want ~59.4", rt.ArrivalSoC)
	}
	w := rt.Waypoints
	if len(w) != 2 || w[0].WaypointType != "ORIGIN" || w[1].WaypointType != "DESTINATION" {
		t.Fatalf("waypoints = %+v", w)
	}
	// Departs at Now truncated to the minute; Valhalla's time for the drive.
	if w[0].DepartureTimeUTC != "2026-12-01T08:00:00Z" || w[1].ArrivalTimeUTC != "2026-12-01T09:00:00Z" {
		t.Errorf("times = %s / %s", w[0].DepartureTimeUTC, w[1].ArrivalTimeUTC)
	}
	if rt.TotalDriveDurationSec != 3600 || rt.TotalChargingDurationSec != 0 || rt.TotalTripDurationSec != 3600 || rt.TotalDriveDistanceMeters != 100000 {
		t.Errorf("totals = %+v", rt)
	}
	if math.Abs(rt.EnergyConsumptionKWh-27.0) > 0.5 { // 100 km / (2.3*1.609) = 27.0 kWh
		t.Errorf("energy = %v kWh, want ~27", rt.EnergyConsumptionKWh)
	}
	if got.SoCBelowLimit {
		t.Error("arrives with 59%, not below the limit")
	}
}

func TestLongTripChargesAtTheFurthestReachableStation(t *testing.T) {
	// 500 km needs ~103 points from 80: one stop. With the 10 % reserve the
	// truck can drive (80-10)/0.2062 = 339 m... = 339 km before it must charge.
	r := &fakeRouter{lat0: 34, legKm: []float64{500}, speedKmh: 100}
	poi := []chargers.POI{
		stationAt(34, 100, 150, "Early", ""),
		stationAt(34, 300, 150, "Mid", ""),
		stationAt(34, 330, 150, "Far", ""), // furthest that can still be reached with the reserve
		stationAt(34, 400, 150, "Too far", ""),
	}
	got, err := newPlanner(r, fakeChargers{pois: poi}).PlanTrip(context.Background(), input(80, 2))
	if err != nil {
		t.Fatal(err)
	}
	st := stopsOf(got)
	if len(st) != 1 || st[0].Name != "Far" {
		t.Fatalf("stops = %+v, want one stop at Far", st)
	}
	s := st[0]
	// Arrival at 330 km: 80 - 330000*0.0002062 = 11.9.
	if math.Abs(s.ArrivalSoC-11.9) > 0.3 || s.DepartureSoC <= s.ArrivalSoC || s.DepartureSoC > 90 {
		t.Errorf("stop SoC arrive %v depart %v", s.ArrivalSoC, s.DepartureSoC)
	}
	// 170 km left needs 35 points + the reserve 10 = 45, and the trip can be
	// finished from this one charge, so it charges to 45 plus the 2-point margin,
	// not to 80.
	if s.DepartureSoC != 48 {
		t.Errorf("charge target = %v, want 48 (what the rest needs plus the margin)", s.DepartureSoC)
	}
	// Duration: (48-11.9)/100*131 kWh at min(150,220)*0.62 kW.
	want := (s.DepartureSoC - s.ArrivalSoC) / 100 * 131 / (150 * 0.62) * 3600
	if math.Abs(float64(s.ChargeDurationSec)-want) > 90 {
		t.Errorf("charge time = %ds, want ~%.0fs", s.ChargeDurationSec, want)
	}
	if s.MaxPowerKW != 150 || s.DistanceFromOriginMeters < 329000 || s.DistanceFromOriginMeters > 331000 {
		t.Errorf("stop = %+v", s)
	}
	rt := got.Routes[0]
	if !rt.DestinationReached || rt.ArrivalSoC < 10 {
		t.Errorf("destination %v arrival %v", rt.DestinationReached, rt.ArrivalSoC)
	}
	// Time: 500 km at 100 km/h plus the charge.
	if rt.TotalChargingDurationSec != s.ChargeDurationSec || rt.TotalTripDurationSec != 18000+s.ChargeDurationSec {
		t.Errorf("totals = %+v", rt)
	}
	// The departure from the station is its arrival plus the charge.
	arr, _ := time.Parse(time.RFC3339, s.ArrivalTimeUTC)
	dep, _ := time.Parse(time.RFC3339, s.DepartureTimeUTC)
	if dep.Sub(arr) != time.Duration(s.ChargeDurationSec)*time.Second {
		t.Errorf("dwell = %v for a %ds charge", dep.Sub(arr), s.ChargeDurationSec)
	}
	if !got.ChargeStationsAvailable {
		t.Error("stations were available")
	}
}

func TestTargetArrivalSoCChangesTheReserve(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{300}, speedKmh: 100}
	poi := []chargers.POI{stationAt(34, 200, 150, "Mid", "")}
	// 300 km costs ~61.9 points. From 80 that arrives at ~18: fine for the
	// default reserve of 10, but not for a requested 25.
	in := input(80, 2)
	if got, _ := newPlanner(r, fakeChargers{pois: poi}).PlanTrip(context.Background(), in); len(stopsOf(got)) != 0 {
		t.Error("default reserve: no stop expected")
	}
	target := 25.0
	in.TargetArrivalSocPercent = &target
	got, _ := newPlanner(r, fakeChargers{pois: poi}).PlanTrip(context.Background(), in)
	if len(stopsOf(got)) != 1 || got.Routes[0].ArrivalSoC < 25 {
		t.Errorf("target 25: stops %d arrival %v", len(stopsOf(got)), got.Routes[0].ArrivalSoC)
	}
}

func TestNoReachableStationIsReportedHonestly(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{500}, speedKmh: 100}
	// The only station is past the point where the charge runs out.
	got, err := newPlanner(r, fakeChargers{pois: []chargers.POI{stationAt(34, 480, 150, "Distant", "")}}).PlanTrip(context.Background(), input(80, 2))
	if err != nil {
		t.Fatal(err)
	}
	rt := got.Routes[0]
	if rt.DestinationReached || !got.SoCBelowLimit || len(stopsOf(got)) != 0 {
		t.Fatalf("plan = %+v", got)
	}
	// SoC hits zero at 80/0.0002062 = 388 km.
	if math.Abs(rt.BatteryEmptyToDestMeters-112000) > 2500 {
		t.Errorf("battery empty %v m from the destination, want ~112000", rt.BatteryEmptyToDestMeters)
	}
	if rt.BatteryEmptyLat < 36.4 || rt.BatteryEmptyLat > 37.7 { // 34 + 388/111.2 = 37.49
		t.Errorf("battery empty at lat %v", rt.BatteryEmptyLat)
	}
	if rt.ArrivalSoC != 0 {
		t.Errorf("arrival SoC = %v", rt.ArrivalSoC)
	}
	// No charger data at all behaves the same, and says no stations are available.
	got, _ = newPlanner(r, nil).PlanTrip(context.Background(), input(80, 2))
	if got.Routes[0].DestinationReached || got.ChargeStationsAvailable {
		t.Errorf("no archive: %+v", got)
	}
}

func TestPreferredNetworkWinsAmongNearTies(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{500}, speedKmh: 100}
	poi := []chargers.POI{
		stationAt(34, 330, 150, "Other", "Blink"),
		stationAt(34, 325, 150, "Electrify America Pit Stop", "Electrify America"), // 5 km before: a near tie
		stationAt(34, 250, 350, "Faster but far behind", "Electrify America"),      // 80 km before: not a tie
	}
	in := input(80, 2)
	if st := stopsOf(mustPlan(t, newPlanner(r, fakeChargers{pois: poi}), in)); st[0].Name != "Other" {
		t.Errorf("without preferences the furthest wins, got %q", st[0].Name)
	}
	in.NetworkPreferences = []rivian.NetworkPreference{{NetworkID: "10027", Preference: 1}} // Electrify America
	if st := stopsOf(mustPlan(t, newPlanner(r, fakeChargers{pois: poi}), in)); st[0].Name != "Electrify America Pit Stop" {
		t.Errorf("a preferred network within 10 km of the best should win, got %q", st[0].Name)
	}
}

func mustPlan(t *testing.T, p *Planner, in rivian.PlanTripInput) *rivian.TripPlan {
	t.Helper()
	got, err := p.PlanTrip(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestViasAreKeptInOrderWithTheirOwnTimes(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{50, 70}, speedKmh: 100}
	got := mustPlan(t, newPlanner(r, fakeChargers{}), input(80, 3))
	w := got.Routes[0].Waypoints
	if len(w) != 3 || w[1].WaypointType != "WAYPOINT" {
		t.Fatalf("waypoints = %+v", w)
	}
	if w[1].ArrivalTimeUTC != "2026-12-01T08:30:00Z" || w[1].DepartureTimeUTC != "2026-12-01T08:30:00Z" ||
		w[1].DistanceFromOriginMeters != 50000 || w[2].ArrivalTimeUTC != "2026-12-01T09:12:00Z" {
		t.Errorf("via = %+v dest arrival %s", w[1], w[2].ArrivalTimeUTC)
	}
	if w[1].Latitude != 35 {
		t.Errorf("a via keeps the requested coordinates, got %v", w[1].Latitude)
	}
}

func TestDriveModeTrailerAndDeparture(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{100}, speedKmh: 100}
	base := mustPlan(t, newPlanner(r, fakeChargers{}), input(80, 2)).Routes[0].EnergyConsumptionKWh
	for mode, factor := range map[string]float64{"DISTANCE": 0.95, "SPORT": 1.10, "EVERYDAY": 1.0} {
		in := input(80, 2)
		in.DriveMode = mode
		got := mustPlan(t, newPlanner(r, fakeChargers{}), in).Routes[0].EnergyConsumptionKWh
		if math.Abs(got-base*factor) > 0.2 {
			t.Errorf("%s: %v kWh, want %v", mode, got, base*factor)
		}
	}
	in := input(80, 2)
	in.TrailerProfile = "small"
	if got := mustPlan(t, newPlanner(r, fakeChargers{}), in).Routes[0].EnergyConsumptionKWh; math.Abs(got-base*1.6) > 0.3 {
		t.Errorf("trailer: %v kWh, want %v", got, base*1.6)
	}
	in = input(80, 2)
	in.DepartureTime = time.Date(2026, 12, 25, 6, 30, 0, 0, time.UTC)
	if d := mustPlan(t, newPlanner(r, fakeChargers{}), in).Routes[0].Waypoints[0].DepartureTimeUTC; d != "2026-12-25T06:30:00Z" {
		t.Errorf("explicit departure = %s", d)
	}
}

func TestDeterministicAndReusable(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{500}, speedKmh: 100}
	c := fakeChargers{pois: []chargers.POI{stationAt(34, 330, 150, "A", ""), stationAt(34, 330, 150, "B", "")}} // an exact tie
	p := newPlanner(r, c)
	a := mustPlan(t, p, input(80, 2))
	b := mustPlan(t, p, input(80, 2))
	if !reflect.DeepEqual(a, b) {
		t.Error("same input must give the same plan, including tie-breaks")
	}
}

func TestInvalidRequestsAndFailures(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{100}, speedKmh: 100}
	p := newPlanner(r, fakeChargers{})
	for name, in := range map[string]rivian.PlanTripInput{
		"one waypoint":     input(80, 1),
		"eleven waypoints": input(80, 11),
		"zero soc":         input(0, 2),
		"over 100 soc":     input(101, 2),
	} {
		if _, err := p.PlanTrip(context.Background(), in); !errors.Is(err, rivian.ErrInvalidTrip) {
			t.Errorf("%s: err = %v, want ErrInvalidTrip", name, err)
		}
	}
	if r.calls != 0 {
		t.Error("an invalid request must not reach the router")
	}
	if _, err := newPlanner(&fakeRouter{err: errors.New("down")}, nil).PlanTrip(context.Background(), input(80, 2)); err == nil || errors.Is(err, rivian.ErrInvalidTrip) {
		t.Errorf("a routing failure is an error that is not ErrInvalidTrip: %v", err)
	}
	if _, err := newPlanner(r, fakeChargers{err: errors.New("archive")}).PlanTrip(context.Background(), input(80, 2)); err == nil {
		t.Error("a charger archive failure must surface")
	}
	// Ten waypoints are fine.
	r10 := &fakeRouter{lat0: 34, legKm: []float64{10, 10, 10, 10, 10, 10, 10, 10, 10}, speedKmh: 100}
	if _, err := newPlanner(r10, fakeChargers{}).PlanTrip(context.Background(), input(80, 10)); err != nil {
		t.Errorf("ten waypoints: %v", err)
	}
}

func TestLazyArchiveWithoutAnArchive(t *testing.T) {
	if _, err := (LazyArchive{}).QueryCorridor([][2]float64{{1, 1}, {2, 2}}, chargers.FilterDCFC, chargers.QueryCorridorOptions{}); err == nil {
		t.Error("a nil archive must be an error, not a panic")
	}
}

// The replay's planner: a stored plan when one matches, a synthetic plan
// otherwise. Both are simulated; neither touches the network.
func TestStoredPlanFirstThenSynthetic(t *testing.T) {
	fx, err := demo.PlanFixtures()
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRouter{lat0: 34, legKm: []float64{100}, speedKmh: 100}
	first := rivian.FirstPlanner{rivian.NewFixturePlanner(fx, func() time.Time { return pinned }), newPlanner(r, fakeChargers{})}

	// LA to San Diego at 80 % is a stored plan: the router is not asked.
	f := fx[2]
	got, err := first.PlanTrip(context.Background(), rivian.PlanTripInput{StartingSoC: f.StartSoC, Waypoints: []rivian.PlanTripWaypoint{
		{Latitude: f.Origin[0], Longitude: f.Origin[1]}, {Latitude: f.Destination[0], Longitude: f.Destination[1]},
	}})
	if err != nil || !got.Simulated || r.calls != 0 {
		t.Fatalf("stored: %v, router calls %d, err %v", got, r.calls, err)
	}
	// Any other trip is synthesized.
	got, err = first.PlanTrip(context.Background(), input(80, 2))
	if err != nil || !got.Simulated || r.calls != 1 {
		t.Fatalf("synthetic: %v, router calls %d, err %v", got, r.calls, err)
	}
}

// The last stop is a top-up when the destination is close (qa: 14.8 mi to go
// from 11.3 % must not charge for 40 minutes to 80 %), and the usual 80 only
// when the rest of the trip cannot be finished from one charge.
func TestFinalStopChargesOnlyWhatTheRestNeeds(t *testing.T) {
	r := &fakeRouter{lat0: 34, legKm: []float64{500}, speedKmh: 100}
	// A station 476 km along leaves 24 km to the destination: ~5 points + 10 reserve.
	near := fakeChargers{pois: []chargers.POI{stationAt(34, 330, 150, "Near end", "")}}
	got := mustPlan(t, newPlanner(r, near), input(80, 2))
	s := stopsOf(got)[0]
	if s.DepartureSoC > 50 {
		t.Errorf("charged to %v with 170 km to go", s.DepartureSoC)
	}
	if arr := got.Routes[0].ArrivalSoC; arr < 10 || arr > 14 {
		t.Errorf("arrives with %v, want just above the 10 reserve", arr)
	}
	// 800 km with stops at 330 and 640: each stop charges only what the leg to
	// the next stop (or the end) needs plus the reserve and margin. The first
	// needs 310 km = 63.9 points + 10 + 2, so ~76, not a blanket 80; the second,
	// 160 km from the end, is a top-up.
	long := &fakeRouter{lat0: 34, legKm: []float64{800}, speedKmh: 100}
	far := fakeChargers{pois: []chargers.POI{stationAt(34, 330, 150, "First", ""), stationAt(34, 640, 150, "Second", "")}}
	st := stopsOf(mustPlan(t, newPlanner(long, far), input(80, 2)))
	if len(st) != 2 || math.Abs(st[0].DepartureSoC-76) > 1 {
		t.Fatalf("stops = %+v, want a first charge to ~76", st)
	}
	if math.Abs(st[1].ArrivalSoC-12) > 1 {
		t.Errorf("the second stop arrives with %v, want the reserve plus the margin (~12)", st[1].ArrivalSoC)
	}
	if st[1].DepartureSoC > 50 {
		t.Errorf("the last stop charged to %v, want a top-up for 160 km", st[1].DepartureSoC)
	}
	// A trip that needs more than the cap from the first stop can't be settled by
	// one charge: the stop charges to what reaches the next one.
	huge := &fakeRouter{lat0: 34, legKm: []float64{1200}, speedKmh: 100}
	hs := stopsOf(mustPlan(t, newPlanner(huge, fakeChargers{pois: []chargers.POI{
		stationAt(34, 330, 150, "A", ""), stationAt(34, 640, 150, "B", ""), stationAt(34, 950, 150, "C", "")}}), input(80, 2)))
	if len(hs) != 3 {
		t.Fatalf("1200 km = %d stops, want 3", len(hs))
	}
	for i, w := range hs[:2] {
		if w.DepartureSoC < 70 || w.DepartureSoC > 90 {
			t.Errorf("stop %d charges to %v, want enough for the next 310 km", i, w.DepartureSoC)
		}
	}
	// Always at least a point above the arrival, never above the cap.
	for _, w := range st {
		if w.DepartureSoC <= w.ArrivalSoC || w.DepartureSoC > 90 {
			t.Errorf("stop %+v", w)
		}
	}
}

func TestNoRoadRouteIsAnInvalidTripNotAServerError(t *testing.T) {
	_, err := newPlanner(&fakeRouter{err: maps.ErrNoRoute}, nil).PlanTrip(context.Background(), input(80, 2))
	if !errors.Is(err, rivian.ErrInvalidTrip) || !strings.Contains(err.Error(), "no road route") {
		t.Errorf("err = %v, want ErrInvalidTrip saying there is no road route", err)
	}
}
