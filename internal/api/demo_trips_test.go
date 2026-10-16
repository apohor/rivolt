package api

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/demo"
)

func demoTripsRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/api/demo/trips", handleDemoTrips(nil))
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

func TestResolveDemoIndexLinksSavedTripByName(t *testing.T) {
	id := uuid.New()
	base := demo.PresetBlob{Body: []byte(`{"trips":[
		{"id":"las-vegas","name":"Vegas","saved_trip_name":"Las Vegas weekend","shape":"abc"},
		{"id":"san-diego","name":"SD"},
		{"id":"ghost","name":"G","saved_trip_name":"No such trip"}]}`)}
	out := resolveDemoIndex(context.Background(), base, func(context.Context) (map[string]uuid.UUID, error) {
		return map[string]uuid.UUID{"Las Vegas weekend": id, "Someone else's trip": uuid.New()}, nil
	})
	var doc struct {
		Trips []map[string]any `json:"trips"`
	}
	if err := json.Unmarshal(out.Body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Trips[0]["saved_trip_id"] != id.String() || doc.Trips[0]["shape"] != "abc" {
		t.Errorf("linked entry = %v", doc.Trips[0])
	}
	if _, ok := doc.Trips[1]["saved_trip_id"]; ok {
		t.Error("entry without a name got an id")
	}
	if _, ok := doc.Trips[2]["saved_trip_id"]; ok {
		t.Error("unknown name got an id")
	}
	if out.ETag == "" || out.ETag == base.ETag {
		t.Errorf("etag = %q", out.ETag)
	}
	// A lookup error serves the index unchanged.
	bad := resolveDemoIndex(context.Background(), base, func(context.Context) (map[string]uuid.UUID, error) {
		return nil, errors.New("db down")
	})
	if string(bad.Body) != string(base.Body) {
		t.Error("lookup failure altered the index")
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
