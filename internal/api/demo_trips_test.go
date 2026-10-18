package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
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
	multi := 0
	for _, tr := range idx.Trips {
		b, ok := demo.PresetPlan(tr.ID)
		if !ok {
			t.Errorf("preset %s has no plan", tr.ID)
			continue
		}
		// A multi-day preset's plan is a saved multi-day snapshot, as the
		// planner saves one; its days carry their own road shapes.
		if days, ok := savedMultiday(b.Body); ok {
			multi++
			if tr.Name == "" || len(tr.Inputs) == 0 || len(tr.Summary) == 0 {
				t.Errorf("preset %s: name/inputs/summary missing", tr.ID)
			}
			if err := validateSavedPlan(b.Body); err != nil || len(days) < 2 {
				t.Errorf("preset %s: %d days, %v", tr.ID, len(days), err)
			}
			for n := 1; n <= len(days); n++ {
				sub, ok := subjectForDay(days, n)
				if !ok {
					t.Errorf("preset %s: no day %d", tr.ID, n)
					continue
				}
				var dp tripPlanResponse
				if err := json.Unmarshal(sub.plan, &dp); err != nil || dp.TripPlan == nil || len(dp.Routes) != 1 || len(dp.Costs) != 1 {
					t.Errorf("preset %s day %d plan: %v", tr.ID, n, err)
				}
			}
			continue
		}
		if tr.Name == "" || len(tr.Inputs) == 0 || len(tr.Summary) == 0 || tr.RoutePolyline == "" {
			t.Errorf("preset %s: name/inputs/summary/route_polyline missing", tr.ID)
		}
		// The stored plan must be the planner's own response shape.
		dec := json.NewDecoder(bytes.NewReader(b.Body))
		dec.DisallowUnknownFields()
		var plan tripPlanResponse
		if err := dec.Decode(&plan); err != nil {
			t.Errorf("preset %s plan does not decode as tripPlanResponse: %v", tr.ID, err)
		}
	}
	if multi != 1 {
		t.Errorf("%d multi-day presets, want the two-day Las Vegas trip", multi)
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
