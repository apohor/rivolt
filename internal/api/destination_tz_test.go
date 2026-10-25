package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// A single-day plan carries the zone at its destination (San Diego in planWith), so an arrival can be shown on that clock.
func TestSinglePlanCarriesTheDestinationZone(t *testing.T) {
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: &daysPlanner{}}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planWith(""))))
	var out struct {
		DestinationTZ string `json:"destination_tz"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.DestinationTZ != "America/Los_Angeles" {
		t.Errorf("%d destination_tz = %q (%s), want America/Los_Angeles", rec.Code, out.DestinationTZ, rec.Body.String())
	}
}

// A multi-day trip across zones: each day's end_tz is where that day ends (Phoenix, which keeps no daylight time, then Denver), and
// destination_tz is the final destination's.
func TestMultidayPlanCarriesEachDaysZoneAndTheDestinationZone(t *testing.T) {
	code, out := runMultiday(t, &daysPlanner{}, `{`+mdVehicle+`"start_at":"2026-10-20T08:00:00Z",
	  "origin":{"latitude":34.05,"longitude":-118.24},
	  "days":[{"end":{"latitude":33.4484,"longitude":-112.0740},"overnight":{"l2_kw":7,"until":"07:00","max_soc_pct":80}},
	          {"end":{"latitude":39.7392,"longitude":-104.9903}}]}`)
	if code != 200 {
		t.Fatalf("status %d: %v", code, out["_body"])
	}
	days := out["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("%d days: %v", len(days), out["_body"])
	}
	if got := days[0].(map[string]any)["end_tz"]; got != "America/Phoenix" {
		t.Errorf("day 1 end_tz = %v, want America/Phoenix", got)
	}
	if got := days[1].(map[string]any)["end_tz"]; got != "America/Denver" {
		t.Errorf("day 2 end_tz = %v, want America/Denver", got)
	}
	if out["destination_tz"] != "America/Denver" {
		t.Errorf("destination_tz = %v, want America/Denver", out["destination_tz"])
	}
}
