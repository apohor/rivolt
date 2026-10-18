package rivian

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func departureInput() PlanTripInput {
	tt := true
	tgt := 15.0
	return PlanTripInput{
		VehicleID: "v1", StartingSoC: 80, StartingRangeMeters: 300000, TargetArrivalSocPercent: &tgt,
		DriveMode: "EVERYDAY", HasAdapter: &tt,
		Waypoints: []PlanTripWaypoint{{Latitude: 32.7767, Longitude: -96.797}, {Latitude: 29.4241, Longitude: -98.4936}},
	}
}

// planTripGolden is the query as sent before the departure argument existed,
// for an input that sets none of the optional ones: it must never change.
const planTripGolden = "query planTripWithMultiStopV2 {\n  planTrip2(waypoints: [{latitude: 32.7767, longitude: -96.797}, {latitude: 29.4241, longitude: -98.4936}], vehicle: \"v1\", startingSoc: 80.0, startingRangeMeters: 300000.0, targetArrivalSocPercent: 15.0, driveMode: EVERYDAY, hasAdapter: true) {\n    status\n    plans {\n      summary {\n        destinationReachable\n        socBelowLimitAtDestination\n        totalChargeDurationSeconds\n        totalDriveDurationSeconds\n        totalDriveDistanceMeters\n        totalTripDurationSeconds\n        arrivalSOCPercent\n        arrivalRangeMeters\n        arrivalEnergyKwh\n      }\n      waypoints {\n        waypointType\n        latitude\n        longitude\n        arrivalSOCPercent\n        departureSOCPercent\n        arrivalRangeMeters\n        departureRangeMeters\n        totalTimeAtWaypointSeconds\n        arrivalTimeUTC\n        departureTimeUTC\n        distanceFromOriginMeters\n        charger {\n          adapterRequired\n          entityId\n          name\n          maxPowerKw\n          chargeDurationSeconds\n        }\n      }\n    }\n  }\n}"

func TestPlanTripQueryUnchangedWhenDepartureUnset(t *testing.T) {
	in := departureInput()
	if q, _ := buildPlanTrip2Query(in); q != planTripGolden {
		t.Errorf("the request changed with no departure set:\n%s", q)
	}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	// A departure that is now or already past is "now": omitted, byte-identical.
	for name, dep := range map[string]time.Time{
		"zero": {}, "past": now.Add(-time.Hour), "exactly now": now,
	} {
		in.DepartureTime = dep
		if q, _ := buildPlanTrip2QueryAt(in, now); q != planTripGolden {
			t.Errorf("%s departure changed the request:\n%s", name, q)
		}
	}
}

func TestPlanTripQuerySendsAFutureDepartureAsEpochMillis(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	in := departureInput()
	in.DepartureTime = now.Add(26 * time.Hour).In(time.FixedZone("x", -7*3600))
	q, _ := buildPlanTrip2QueryAt(in, now)
	want := fmt.Sprintf("departureTime: %d", now.Add(26*time.Hour).UnixMilli())
	if strings.Count(q, "departureTime:") != 1 || !strings.Contains(q, want) {
		t.Errorf("want one %q, got:\n%s", want, q)
	}
	// The one-millisecond-after boundary counts as the future.
	in.DepartureTime = now.Add(time.Millisecond)
	if q, _ := buildPlanTrip2QueryAt(in, now); !strings.Contains(q, "departureTime:") {
		t.Error("a departure just after now is in the future")
	}
	// It composes with the other arguments, which are untouched.
	if !strings.Contains(q, "driveMode: EVERYDAY") || !strings.Contains(q, "hasAdapter: true") {
		t.Errorf("other arguments lost:\n%s", q)
	}
	// Network preferences still use the variable form.
	in.NetworkPreferences = []NetworkPreference{{NetworkID: "10027"}}
	q, vars := buildPlanTrip2QueryAt(in, now)
	if vars == nil || !strings.Contains(q, "departureTime:") || !strings.Contains(q, "$networkPreferences") {
		t.Errorf("departure with preferences:\n%s\n%v", q, vars)
	}
}

func TestLivePlanTripRefusesMoreThanTenWaypoints(t *testing.T) {
	// A local gateway that records whether it was asked anything.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewLive().WithEndpoint(srv.URL)
	c.userSessionToken = "token"
	mk := func(n int) PlanTripInput {
		in := departureInput()
		in.Waypoints = nil
		for i := 0; i < n; i++ {
			in.Waypoints = append(in.Waypoints, PlanTripWaypoint{Latitude: 30 + float64(i)/10, Longitude: -97})
		}
		return in
	}
	_, err := c.PlanTrip(context.Background(), mk(MaxPlanWaypoints+1))
	if !errors.Is(err, ErrInvalidTrip) || !strings.Contains(err.Error(), "11 waypoints") {
		t.Errorf("11 waypoints = %v, want ErrInvalidTrip naming the count", err)
	}
	if hits.Load() != 0 {
		t.Error("an over-long request must not reach the gateway")
	}
	// Ten is allowed: it gets past the check and is sent (the stub gateway then fails it).
	if _, err := c.PlanTrip(context.Background(), mk(MaxPlanWaypoints)); errors.Is(err, ErrInvalidTrip) || hits.Load() == 0 {
		t.Errorf("10 waypoints must be accepted and sent: %v (gateway hits %d)", err, hits.Load())
	}
}

func TestLivePlanTripRefusesADepartureMoreThanThirtyDaysAhead(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewLive().WithEndpoint(srv.URL)
	c.userSessionToken = "token"

	for name, dep := range map[string]time.Time{
		"31 days":   time.Now().Add(31 * 24 * time.Hour),
		"a year":    time.Now().AddDate(1, 0, 0),
		"year 9999": time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		in := departureInput()
		in.DepartureTime = dep
		_, err := c.PlanTrip(context.Background(), in)
		if !errors.Is(err, ErrInvalidTrip) || !strings.Contains(err.Error(), "30 days ahead") {
			t.Errorf("%s: err = %v, want ErrInvalidTrip naming the limit", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("a far-future departure reached the gateway %d times", hits.Load())
	}
	// 29 days ahead is fine and is sent, as a departureTime argument.
	in := departureInput()
	in.DepartureTime = time.Now().Add(29 * 24 * time.Hour)
	if _, err := c.PlanTrip(context.Background(), in); errors.Is(err, ErrInvalidTrip) || hits.Load() != 1 {
		t.Errorf("29 days must be accepted and sent: %v (hits %d)", err, hits.Load())
	}
}

func TestPlanTripQueryRoutePreferences(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	in := departureInput()
	// Nil: the request never mentions it, byte-identical to before.
	if q, _ := buildPlanTrip2QueryAt(in, now); q != planTripGolden || strings.Contains(q, "routePreferences") {
		t.Errorf("a trip with no preference changed the request:\n%s", q)
	}
	in.RoutePreferences = &RoutePreferences{AvoidTolls: true, AvoidHighways: true}
	q, _ := buildPlanTrip2QueryAt(in, now)
	want := "routePreferences: {avoidTolls: true, avoidFerries: false, avoidHighways: true}"
	if strings.Count(q, "routePreferences") != 1 || !strings.Contains(q, want) {
		t.Errorf("want one %q in:\n%s", want, q)
	}
	// It composes with the departure and the other arguments.
	in.DepartureTime = now.Add(48 * time.Hour)
	q, _ = buildPlanTrip2QueryAt(in, now)
	if !strings.Contains(q, "departureTime:") || !strings.Contains(q, want) || !strings.Contains(q, "driveMode: EVERYDAY") {
		t.Errorf("composed query:\n%s", q)
	}
	// Any() is what the handlers use to decide whether there is a preference.
	if (*RoutePreferences)(nil).Any() || (&RoutePreferences{}).Any() || !(&RoutePreferences{AvoidFerries: true}).Any() {
		t.Error("Any()")
	}
}
