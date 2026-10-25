package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripmultiday"
	"github.com/apohor/rivolt/internal/trips"
)

// The trip planner's iPhone fixtures are the server's own answers, marshaled from its own response types, so a change to a type
// (or to a status or error body) fails here until the fixture is regenerated: RIVOLT_WRITE_FIXTURES=1 go test ./internal/api -run
// TestTripFixtures. The data is invented (Seattle to Spokane, a made-up charger and made-up names): no real vehicle, account or
// address. Note for the Swift side: rivian.TripPlan has NO json tags, so a plan's own keys are Go-cased ("Routes",
// "TotalDriveDurationSec", "Waypoints"...) while everything around it is snake_case. That is how the server sends it today.
const tripFixturesDir = "../../ios/RivoltTests/Fixtures/"

func ptrF(v float64) *float64 { return &v }

func fixtureMoment(hour, min int) time.Time {
	return time.Date(2027, 1, 12, hour, min, 0, 0, time.UTC)
}

func fixturePlan() *rivian.TripPlan {
	return &rivian.TripPlan{
		Status: "SUCCESS", ChargeStationsAvailable: true,
		Routes: []rivian.TripRoute{{
			DestinationReached: true, TotalChargingDurationSec: 1500, TotalDriveDurationSec: 16200, TotalTripDurationSec: 17700,
			TotalDriveDistanceMeters: 448000, ArrivalSoC: 22, ArrivalReachableMeters: 61000, EnergyConsumptionKWh: 168.2,
			RouteResponseRaw: map[string]any{"geometry": "placeholder-encoded-polyline", "precision": 6},
			Waypoints: []rivian.PlannedWaypoint{
				{WaypointType: "origin", Name: "Example Origin", Latitude: 47.6205, Longitude: -122.3493, ArrivalSoC: 88, DepartureSoC: 88, DepartureTimeUTC: "2027-01-12T16:00:00Z"},
				{WaypointType: "charger", EntityID: "00000000-0000-4000-8000-0000000000c1", Name: "Example Fast Charge, Ellensburg", Latitude: 46.9965, Longitude: -120.5478,
					MaxPowerKW: 200, ChargeDurationSec: 1500, ArrivalSoC: 31, DepartureSoC: 72, ArrivalReachableMeters: 98000, DepartureReachableMeters: 301000,
					ArrivalTimeUTC: "2027-01-12T18:10:00Z", DepartureTimeUTC: "2027-01-12T18:35:00Z", DistanceFromOriginMeters: 172000},
				{WaypointType: "destination", Name: "Example Destination", Latitude: 47.6588, Longitude: -117.4260, ArrivalSoC: 22, ArrivalReachableMeters: 61000,
					ArrivalTimeUTC: "2027-01-12T20:55:00Z", DistanceFromOriginMeters: 448000},
			},
		}},
	}
}

func fixtureCosts() []tripadvice.CostEstimate {
	return []tripadvice.CostEstimate{{Currency: "USD", DCFCSpendLow: 11.5, DCFCSpendHigh: 14.0, PricedStops: 1, EstimateBasis: "observed", HomeEquivalent: 9.8,
		GasEquivalent: 52.1, GasPricePerGallon: 3.9, GasMPG: 24, HomeRateUsed: 0.14}}
}

func fixtureDay(index int, from, to string, dep, arr time.Time, overnight *tripmultiday.OvernightResult) tripPlanMultidayDay {
	return tripPlanMultidayDay{Day: tripmultiday.Day{
		Index: index, Plan: fixturePlan(), DepartAt: dep, ArriveAt: arr,
		Start: tripmultiday.Waypoint{Latitude: 47.6205, Longitude: -122.3493, Label: from}, End: tripmultiday.Waypoint{Latitude: 47.6588, Longitude: -117.4260, Label: to},
		DepartureSoC: 88, ArrivalSoC: 22, DestinationReached: true, Overnight: overnight,
	}, Costs: fixtureCosts()}
}

func fixtureMultiday() tripPlanMultidayResponse {
	night := &tripmultiday.OvernightResult{Name: "Example Hotel with L2", ParkedHours: 11, L2KW: 7.2, AddedKWh: 56, PostChargeSoCPct: 80}
	return tripPlanMultidayResponse{
		Days: []tripPlanMultidayDay{
			fixtureDay(1, "Example Origin", "Example Hotel", fixtureMoment(16, 0), fixtureMoment(21, 0), night),
			fixtureDay(2, "Example Hotel", "Example Destination", fixtureMoment(40, 0), fixtureMoment(45, 0), nil),
		},
		Total: tripmultiday.Totals{DistanceMeters: 896000, DriveDurationSec: 32400, ChargingDurationSec: 3000}, TZ: "America/Los_Angeles",
	}
}

func fixtureMultidayPartial() tripPlanMultidayResponse {
	r := fixtureMultiday()
	r.Days = r.Days[:1]
	r.Total = tripmultiday.Totals{DistanceMeters: 448000, DriveDurationSec: 16200, ChargingDurationSec: 1500}
	r.FailedDay, r.Error, r.ErrorClass, r.ErrorReason = 2, "Day 2 could not be planned right now. Try again in a minute.", "transient", ""
	return r
}

func fixtureSavedTrip(name string, id uuid.UUID) trips.SavedTrip {
	inputs, _ := json.Marshal(map[string]any{
		"origin": map[string]any{"lat": 47.6205, "lon": -122.3493, "label": "Example Origin"}, "destination": map[string]any{"lat": 47.6588, "lon": -117.4260, "label": "Example Destination"},
		"starting_soc": 88, "pack_kwh": 135,
	})
	plan, _ := json.Marshal(tripPlanResponse{TripPlan: fixturePlan(), Costs: fixtureCosts()})
	return trips.SavedTrip{ID: id, Name: name, Inputs: inputs, Plan: plan, CreatedAt: fixtureMoment(12, 0), UpdatedAt: fixtureMoment(12, 5)}
}

func fixturePlanActualLinked() planActualResponse {
	return planActualResponse{
		Version: 1, SavedTripID: "00000000-0000-4000-8000-0000000000a1", Status: "linked",
		Link:       &planActualLink{TripID: "outing-0001", Confidence: ptrF(0.91), Band: "high", LinkedAt: fixtureMoment(23, 0)},
		Candidates: []ledger.PlanCandidate{},
		Comparison: &ledger.PlanComparison{
			Status: "complete", Currency: "USD", PlanFrom: "saved_plan",
			Leg: ledger.PlanLeg{StartedAt: fixtureMoment(16, 2), ArrivedAt: fixtureMoment(21, 10), Miles: 281.4, DriveIDs: []string{"drive-0001", "drive-0002"}, ChargeIDs: []string{"charge-0001"}},
			Metrics: []ledger.PlanMetric{
				{Metric: "miles", Unit: "mi", Planned: ptrF(278.4), Actual: ptrF(281.4), Delta: ptrF(3), Reasons: []ledger.PlanReason{}},
				{Metric: "cost", Unit: "USD", Planned: ptrF(12.75), Actual: ptrF(15.2), Delta: ptrF(2.45), Reasons: []ledger.PlanReason{{Code: "price_higher", Amount: ptrF(2.45), Detail: "The charger's price was higher than the estimate.", Network: "Example Network"}}},
			},
			Stops: []ledger.PlanStopPair{},
		},
	}
}

func fixturePlanActualNone() planActualResponse {
	return planActualResponse{Version: 1, SavedTripID: "00000000-0000-4000-8000-0000000000a1", Status: "none", Candidates: []ledger.PlanCandidate{}}
}

func fixtureError(f func(w http.ResponseWriter)) (int, http.Header, any) {
	rec := httptest.NewRecorder()
	f(rec)
	var body any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, rec.Header(), body
}

// errorFixture is an error answer as a client sees it: the status, the header a client acts on, and the body.
func errorFixture(f func(w http.ResponseWriter)) map[string]any {
	code, h, body := fixtureError(f)
	out := map[string]any{"status": code, "body": body}
	if ra := h.Get("Retry-After"); ra != "" {
		out["retry_after_header"] = ra
	}
	return out
}

func tripFixtures() map[string]any {
	id1, id2 := uuid.MustParse("00000000-0000-4000-8000-0000000000a1"), uuid.MustParse("00000000-0000-4000-8000-0000000000a2")
	return map[string]any{
		"trip_plan.json":                  tripPlanResponse{TripPlan: fixturePlan(), Costs: fixtureCosts()},
		"trip_plan_multiday.json":         fixtureMultiday(),
		"trip_plan_multiday_partial.json": fixtureMultidayPartial(),
		"trip_saved_list.json":            []trips.SavedTrip{fixtureSavedTrip("Seattle to Spokane", id1), fixtureSavedTrip("Spokane back", id2)},
		"trip_saved_one.json":             fixtureSavedTrip("Seattle to Spokane", id1),
		"trip_plan_actual_linked.json":    fixturePlanActualLinked(),
		"trip_plan_actual_none.json":      fixturePlanActualNone(),
		"trip_plan_actual_multi.json": planActualMultiResponse{Version: 1, SavedTripID: id1.String(), Kind: "multiday",
			Days: []planActualResponse{func() planActualResponse { p := fixturePlanActualLinked(); p.Day = 1; return p }(), func() planActualResponse { p := fixturePlanActualNone(); p.Day = 2; return p }()}},
		"err_plan_quota.json": errorFixture(func(w http.ResponseWriter) { writeQuotaError(w, &planleg.QuotaError{RetryAfter: 37 * time.Minute}) }),
		"err_upstream_user_action.json": errorFixture(func(w http.ResponseWriter) {
			writeUpstreamError(w, &rivian.UpstreamError{Class: rivian.ClassUserAction, Reason: "session expired"})
		}),
		"err_upstream_transient.json": errorFixture(func(w http.ResponseWriter) {
			writeUpstreamError(w, &rivian.UpstreamError{Class: rivian.ClassTransient, Op: "planTripWithMultiStopV2", HTTPStatus: 502})
		}),
		"err_upstream_rate_limited.json": errorFixture(func(w http.ResponseWriter) {
			writeUpstreamError(w, &rivian.UpstreamError{Class: rivian.ClassRateLimited, Op: "planTripWithMultiStopV2", HTTPStatus: 429})
		}),
		"err_no_position.json": errorFixture(noPosition),
	}
}

// Each trip fixture is exactly what the server's types marshal to now.
func TestTripFixturesAreTheServersOwnAnswers(t *testing.T) {
	write := os.Getenv("RIVOLT_WRITE_FIXTURES") != ""
	for name, v := range tripFixtures() {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(tripFixturesDir, name)
		if write {
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with RIVOLT_WRITE_FIXTURES=1)", name, err)
		}
		if !bytes.Equal(want, buf.Bytes()) {
			t.Errorf("%s differs from what the server marshals now; regenerate with RIVOLT_WRITE_FIXTURES=1 go test ./internal/api -run TestTripFixtures", name)
		}
	}
}

// A rate-limited upstream answer carries Retry-After, so a client has one rule for every 429 (the planner quota already did); the
// other upstream answers do not.
func TestUpstreamRateLimitedCarriesRetryAfter(t *testing.T) {
	for class, want := range map[rivian.ErrorClass]string{
		rivian.ClassRateLimited: "60", rivian.ClassTransient: "", rivian.ClassOutage: "", rivian.ClassUserAction: "",
	} {
		rec := httptest.NewRecorder()
		writeUpstreamError(rec, &rivian.UpstreamError{Class: class})
		if got := rec.Header().Get("Retry-After"); got != want {
			t.Errorf("%s: Retry-After %q, want %q", class, got, want)
		}
	}
}
