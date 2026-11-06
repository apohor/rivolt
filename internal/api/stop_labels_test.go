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

type labelledStop struct {
	WaypointType string
	Source       string
	ViaIndex     *int
	ViaEntityID  string
}

func stopsOf(t *testing.T, raw any) []labelledStop {
	t.Helper()
	b, _ := json.Marshal(raw)
	var plan struct {
		Routes []struct{ Waypoints []labelledStop }
	}
	if err := json.Unmarshal(b, &plan); err != nil || len(plan.Routes) == 0 {
		t.Fatalf("no routes in %s (%v)", b, err)
	}
	return plan.Routes[0].Waypoints
}

// The single-day plan labels its stops: origin, the requested via (with the entity id the request attached, echoed back), destination.
func TestSinglePlanLabelsItsStops(t *testing.T) {
	body := `{"vehicle_id":"v1","starting_soc":80,"waypoints":[
	  {"latitude":34.05,"longitude":-118.24,"waypoint_type":"origin"},
	  {"latitude":35.5,"longitude":-117.5,"waypoint_type":"waypoint","entity_id":"poi-42"},
	  {"latitude":32.71,"longitude":-117.16,"waypoint_type":"destination"}]}`
	rec := httptest.NewRecorder()
	handleTripPlan(plannerClient{Client: rivian.NewStub(), TripPlanner: &daysPlanner{}}, nil, nil, uuid.New(), nil, nil, nil, nil)(rec,
		httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	ws := stopsOf(t, out)
	if len(ws) != 3 {
		t.Fatalf("%d stops: %+v", len(ws), ws)
	}
	if ws[0].Source != "origin" || ws[2].Source != "destination" {
		t.Errorf("ends: %q %q", ws[0].Source, ws[2].Source)
	}
	if ws[1].Source != "via" || ws[1].ViaIndex == nil || *ws[1].ViaIndex != 0 || ws[1].ViaEntityID != "poi-42" {
		t.Errorf("the via: %+v", ws[1])
	}
}

// A multi-day plan labels each day's stops with that day's vias, and a day that ends in an overnight stay labels its end "overnight".
func TestMultidayPlanLabelsViasAndTheOvernightEnd(t *testing.T) {
	code, out := runMultiday(t, &daysPlanner{}, `{`+mdVehicle+`"start_at":"2026-10-20T08:00:00Z",
	  "origin":{"latitude":34.05,"longitude":-118.24},
	  "days":[{"vias":[{"latitude":34.9,"longitude":-116.0,"entity_id":"poi-7"}],
	          "end":{"latitude":33.4484,"longitude":-112.0740},"overnight":{"l2_kw":7,"until":"07:00","max_soc_pct":80}},
	          {"end":{"latitude":39.7392,"longitude":-104.9903}}]}`)
	if code != 200 {
		t.Fatalf("status %d: %v", code, out["_body"])
	}
	days := out["days"].([]any)
	d1 := stopsOf(t, days[0].(map[string]any)["plan"])
	var sawVia bool
	for _, s := range d1 {
		if s.Source == "via" && s.ViaEntityID == "poi-7" {
			sawVia = true
		}
	}
	if !sawVia {
		t.Errorf("day 1 has no labelled via: %+v", d1)
	}
	if last := d1[len(d1)-1]; last.Source != "overnight" {
		t.Errorf("day 1 ends in an overnight stay but its last stop is %q", last.Source)
	}
	d2 := stopsOf(t, days[1].(map[string]any)["plan"])
	if last := d2[len(d2)-1]; last.Source != "destination" {
		t.Errorf("the final day's end is %q, want destination", last.Source)
	}
}
