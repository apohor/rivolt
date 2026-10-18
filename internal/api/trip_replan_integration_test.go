//go:build integration

// Re-plan from here against real Postgres: a saved multi-day trip re-planned
// mid-trip keeps the driven days, re-plans the rest from the truck, and maps
// day numbers back; old saves without a stored request are rebuilt from the
// form; refusals are JSON.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/trips"
)

func TestTripReplan_PG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "replan-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "replan-bob")
	if err != nil {
		t.Fatal(err)
	}
	// The truck the reconstructed request resolves to (it carries no vehicle).
	if err := db.UpsertVehicleFromSync(ctx, pool, alice, db.VehicleSync{RivianID: "v1", VIN: "7PDSGABA0RN000001", Name: "Test", Model: "R1S", ModelYear: 2025, PackKWh: 131}); err != nil {
		t.Fatal(err)
	}
	tf := trips.NewFactory(pool)

	req := rpTrip()
	vid := "v1"
	req.VehicleID, req.PackKWh = &vid, 131
	saved := rpSaved()
	days := make([]map[string]any, len(saved))
	for i, d := range saved {
		days[i] = map[string]any{"index": i + 1, "depart_at": d.DepartAt, "arrive_at": d.ArriveAt, "end": d.End,
			"plan": map[string]any{"Routes": []map[string]any{{"TotalDriveDistanceMeters": 250000, "TotalDriveDurationSec": 14400}}}}
	}
	plan, _ := json.Marshal(map[string]any{"kind": "multiday", "request": req, "days": days})
	trip, err := tf.For(alice).Create(ctx, "3 days", json.RawMessage(`{"origin":{"lat":1,"lon":1}}`), plan, nil)
	if err != nil {
		t.Fatal(err)
	}

	call := func(uid any, id string, p *daysPlanner, body string) (int, map[string]any) {
		u := alice
		if uid != nil {
			u = bob
		}
		r := chi.NewRouter()
		r.Post("/trips/saved/{id}/replan", handleTripReplan(plannerClient{Client: rivian.NewStub(), TripPlanner: p}, nil, pool, u, tf.For(u), nil, nil, nil, nil))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/trips/saved/"+id+"/replan", bytes.NewBufferString(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// Day 2, 11:00, near Gilroy with 40 %.
	p := &daysPlanner{}
	body := `{"position":{"lat":37.0,"lon":-121.57},"soc":40,"at":"2026-10-21T11:00:00-07:00"}`
	code, out := call(nil, trip.ID.String(), p, body)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	ds, _ := out["days"].([]any)
	if len(ds) != 3 {
		t.Fatalf("want 3 days (1 done, 2 re-planned), got %d", len(ds))
	}
	for i, want := range []string{"done", "replanned", "replanned"} {
		d := ds[i].(map[string]any)
		if d["status"] != want || d["index"] != float64(i+1) {
			t.Errorf("day %d: status %v index %v", i+1, d["status"], d["index"])
		}
	}
	rp := out["replan"].(map[string]any)
	if rp["from_day"] != float64(2) || rp["phase"] != "driving" || rp["basis"] != "request" {
		t.Errorf("replan block: %v", rp)
	}
	if len(p.calls) != 2 {
		t.Fatalf("want 2 planner calls (days 2 and 3), got %d", len(p.calls))
	}
	if o := p.calls[0].Waypoints[0]; o.Latitude != 37.0 || o.Longitude != -121.57 || p.calls[0].StartingSoC != 40 {
		t.Errorf("day 2 must start at the truck: %+v soc %v", o, p.calls[0].StartingSoC)
	}
	if len(p.calls[1].Waypoints) != 3 {
		t.Errorf("day 3 must keep its via: %d waypoints", len(p.calls[1].Waypoints))
	}

	// An older save without the request is rebuilt from the planner form.
	form := `{"origin":{"lat":34.1613,"lon":-118.1676,"label":"Rose Bowl"},"destination":{"lat":34.1613,"lon":-118.1676,"label":"Rose Bowl"},
	  "extra_stops":[{"lat":35.9894,"lon":-119.9603,"label":"Kettleman City"},{"lat":37.7955,"lon":-122.3937,"label":"Ferry Building"},{"lat":35.9894,"lon":-119.9603,"label":"Kettleman City"}],
	  "overnight_flags":[true,true,false],"overnight_departure_local":"08:00"}`
	old, _ := json.Marshal(map[string]any{"kind": "multiday", "days": days})
	oldTrip, err := tf.For(alice).Create(ctx, "old save", json.RawMessage(form), old, nil)
	if err != nil {
		t.Fatal(err)
	}
	po := &daysPlanner{}
	code, out = call(nil, oldTrip.ID.String(), po, body)
	if code != http.StatusOK {
		t.Fatalf("old save: status %d %v", code, out)
	}
	if rp := out["replan"].(map[string]any); rp["basis"] != "reconstructed" || rp["from_day"] != float64(2) {
		t.Errorf("old save: %v", rp)
	}
	if len(po.calls) != 2 || len(po.calls[1].Waypoints) != 3 || po.calls[0].VehicleID != "v1" {
		t.Errorf("old save re-planned %d days; day 3 waypoints %d", len(po.calls), len(po.calls[len(po.calls)-1].Waypoints))
	}

	// A single-day saved trip is not re-plannable; someone else's trip is not found.
	single, err := tf.For(alice).Create(ctx, "one day", json.RawMessage(`{"a":1}`), json.RawMessage(`{"Routes":[]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, out = call(nil, single.ID.String(), &daysPlanner{}, body); code != http.StatusConflict || out["class"] != "not_multiday" {
		t.Errorf("single-day trip: %d %v", code, out)
	}
	if code, _ = call(true, trip.ID.String(), &daysPlanner{}, body); code != http.StatusNotFound {
		t.Errorf("another user's trip must be 404, got %d", code)
	}
	if code, out = call(nil, trip.ID.String(), &daysPlanner{}, `{"soc":40,"at":"2026-10-21T11:00:00-07:00"}`); code != http.StatusConflict || out["class"] != "position_unknown" {
		t.Errorf("no position and no recent fix: %d %v", code, out)
	}
}
