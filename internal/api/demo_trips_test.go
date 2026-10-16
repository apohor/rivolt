package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/demo"
)

func demoTripsRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/api/demo/trips", handleDemoTrips)
	r.Get("/api/demo/trips/{id}/plan", handleDemoTripPlan)
	return r
}

func TestDemoPresetsEmbeddedDataIsValid(t *testing.T) {
	if err := demo.PresetsErr(); err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Trips []struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Inputs  json.RawMessage `json:"inputs"`
			Summary json.RawMessage `json:"summary"`
		} `json:"trips"`
	}
	if err := json.Unmarshal(demo.PresetsIndex().Body, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Trips) != len(demo.PresetIDs()) {
		t.Fatalf("index has %d trips, loader has %d", len(idx.Trips), len(demo.PresetIDs()))
	}
	for _, tr := range idx.Trips {
		if tr.Name == "" || len(tr.Inputs) == 0 || len(tr.Summary) == 0 {
			t.Errorf("preset %s: name/inputs/summary missing", tr.ID)
		}
		b, ok := demo.PresetPlan(tr.ID)
		if !ok {
			t.Errorf("preset %s has no plan", tr.ID)
			continue
		}
		// The stored plan must be the planner's own response shape.
		dec := json.NewDecoder(bytes.NewReader(b.Body))
		dec.DisallowUnknownFields()
		var plan tripPlanResponse
		if err := dec.Decode(&plan); err != nil {
			t.Errorf("preset %s plan does not decode as tripPlanResponse: %v", tr.ID, err)
		}
	}
}

func TestDemoTripsHandlers(t *testing.T) {
	h := demoTripsRouter()
	get := func(path, inm string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get("/api/demo/trips", "")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") == "" || rec.Header().Get("ETag") == "" ||
		rec.Header().Get(demoHeader) != "1" || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("index: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec2 := get("/api/demo/trips", rec.Header().Get("ETag")); rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
		t.Errorf("conditional = %d, body %d bytes", rec2.Code, rec2.Body.Len())
	}
	for _, bad := range []string{"/api/demo/trips/nope/plan", "/api/demo/trips/..%2F..%2Fetc%2Fpasswd/plan", "/api/demo/trips/INDEX/plan"} {
		if rec := get(bad, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", bad, rec.Code)
		}
	}
	for _, id := range demo.PresetIDs() {
		if rec := get("/api/demo/trips/"+id+"/plan", ""); rec.Code != 200 || !json.Valid(rec.Body.Bytes()) {
			t.Errorf("plan %s = %d", id, rec.Code)
		}
	}
}
