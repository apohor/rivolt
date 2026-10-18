package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/settings"
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
			// Drawn by the planner map in place of a Valhalla route.
			RoutePolyline string `json:"route_polyline"`
		} `json:"trips"`
	}
	if err := json.Unmarshal(demo.PresetsIndex().Body, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Trips) != len(demo.PresetIDs()) {
		t.Fatalf("index has %d trips, loader has %d", len(idx.Trips), len(demo.PresetIDs()))
	}
	for _, tr := range idx.Trips {
		if tr.Name == "" || len(tr.Inputs) == 0 || len(tr.Summary) == 0 || tr.RoutePolyline == "" {
			t.Errorf("preset %s: name/inputs/summary/route_polyline missing", tr.ID)
		}
		b, ok := demo.PresetPlan(tr.ID)
		if !ok {
			t.Errorf("preset %s has no plan", tr.ID)
			continue
		}
		// The stored plan must be the planner's own response shape.
		dec := json.NewDecoder(bytes.NewReader(b.Body))
		dec.DisallowUnknownFields()
		var head struct {
			Days json.RawMessage `json:"days"`
		}
		_ = json.Unmarshal(b.Body, &head)
		if len(head.Days) > 0 {
			// A multi-day preset is a saved multi-day plan, which the picker opens
			// like a saved trip.
			var plan struct {
				Kind  string                `json:"kind"`
				Days  []tripPlanMultidayDay `json:"days"`
				Total json.RawMessage       `json:"total"`
			}
			if err := dec.Decode(&plan); err != nil || plan.Kind != "multiday" || len(plan.Days) == 0 {
				t.Errorf("preset %s plan is not a saved multi-day plan: %v", tr.ID, err)
			}
			if err := validateSavedPlan(b.Body); err != nil {
				t.Errorf("preset %s: validateSavedPlan: %v", tr.ID, err)
			}
			continue
		}
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

var (
	demoEmailRe  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	demoUUIDRe   = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	demoRivianRe = regexp.MustCompile(`\b0[0-9]-[0-9]{9}\b`)
)

// demoPresetMaxBytes bounds one embedded document (a plan is served with
// gzip, but the raw size also bounds memory and egress).
const demoPresetMaxBytes = 512 << 10

// TestDemoPresetFilesAreSmallAndClean checks every embedded preset file
// for size and for identifiers that would not belong in a public demo:
// emails, Rivian vehicle ids, and UUIDs other than the preset ids.
func TestDemoPresetFilesAreSmallAndClean(t *testing.T) {
	blobs := map[string][]byte{"index.json": demo.PresetsIndex().Body}
	allowed := map[string]bool{}
	for _, id := range demo.PresetIDs() {
		allowed[strings.ToLower(id)] = true
		b, ok := demo.PresetPlan(id)
		if !ok {
			t.Fatalf("no plan for %s", id)
		}
		blobs[id+".plan.json"] = b.Body
	}
	for name, body := range blobs {
		if len(body) > demoPresetMaxBytes {
			t.Errorf("%s is %d bytes, over %d", name, len(body), demoPresetMaxBytes)
		}
		if m := demoEmailRe.Find(body); m != nil {
			t.Errorf("%s contains an email address", name)
		}
		if m := demoRivianRe.Find(body); m != nil {
			t.Errorf("%s contains a Rivian vehicle id", name)
		}
		for _, u := range demoUUIDRe.FindAll(body, -1) {
			if !allowed[strings.ToLower(string(u))] {
				t.Errorf("%s contains a UUID that is not a preset id (%s...)", name, string(u)[:8])
			}
		}
	}
}

// The demo's saved two-day Las Vegas trip is a valid saved multi-day plan, and
// each day reads as a plan-vs-actual subject.
func TestDemoMultidayTripIsAValidSavedPlan(t *testing.T) {
	p, err := demo.SampleMultidayPlan(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), time.Time{},
		settings.ChargingConfig{HomePricePerKWh: demo.HomeRate, HomeCurrency: "USD"}, settings.DefaultChargingNetworks())
	if err != nil || p == nil {
		t.Fatalf("plan %v err %v", p, err)
	}
	if err := validateSavedPlan(p.Plan); err != nil {
		t.Fatalf("validateSavedPlan: %v", err)
	}
	days, ok := savedMultiday(p.Plan)
	if !ok || len(days) != 2 {
		t.Fatalf("days %d ok %v", len(days), ok)
	}
	for n := 1; n <= 2; n++ {
		if _, ok := subjectForDay(days, n); !ok {
			t.Errorf("day %d is not a plan-vs-actual subject", n)
		}
	}
}
