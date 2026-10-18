package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
)

type hotelArchive struct{ pois []chargers.POI }

func (h hotelArchive) QueryCorridor(_ [][2]float64, f chargers.Filter, _ chargers.QueryCorridorOptions) ([]chargers.POI, error) {
	if f == chargers.FilterHotels {
		return h.pois, nil
	}
	return nil, nil
}

func straight(km float64) [][2]float64 {
	var p [][2]float64
	for i := 0; i <= 50; i++ {
		p = append(p, [2]float64{34 + km/111.195*float64(i)/50, -118})
	}
	return p
}

func postOvernight(h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/trips/overnight-candidates", bytes.NewReader(b)))
	return rec
}

func TestOvernightCandidatesEndpoint(t *testing.T) {
	noLoad := func(context.Context) error { return nil }
	h := overnightHandler(hotelArchive{pois: []chargers.POI{
		{Name: "Roadside Inn", EntityID: "e1", Lat: 34 + 800/111.195, Lon: -118.003, IsL2: true, MaxPowerKW: 7.2, FacilityType: "HOTEL"},
		{Name: "Fast only", Lat: 34 + 800/111.195, Lon: -118, IsDCFC: true},
	}}, noLoad)
	rec := postOvernight(h, map[string]any{"path": straight(1000), "drive_sec": 36000, "budget_hours": 8})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Candidates []struct {
			Name        string  `json:"name"`
			L2KW        float64 `json:"l2_kw"`
			L2KWAssumed bool    `json:"l2_kw_assumed"`
		} `json:"candidates"`
		Target struct {
			AlongM float64 `json:"along_m"`
		} `json:"target"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Name != "Roadside Inn" || out.Candidates[0].L2KW != 7.2 || out.Candidates[0].L2KWAssumed {
		t.Errorf("candidates = %+v", out.Candidates)
	}
	if out.Target.AlongM < 790_000 || out.Target.AlongM > 810_000 {
		t.Errorf("target = %v m", out.Target.AlongM)
	}
	if !strings.Contains(out.Source, "snapshot") || !strings.Contains(out.Source, "not checked") {
		t.Errorf("the caveat must travel with the list: %q", out.Source)
	}
	// An empty list is [] not null.
	empty := postOvernight(overnightHandler(hotelArchive{}, noLoad), map[string]any{"path": straight(500), "budget_hours": 4})
	if !strings.Contains(empty.Body.String(), `"candidates":[]`) {
		t.Errorf("empty = %s", empty.Body.String())
	}
}

func TestOvernightCandidatesRefusals(t *testing.T) {
	noLoad := func(context.Context) error { return nil }
	h := overnightHandler(hotelArchive{}, noLoad)
	for name, body := range map[string]any{
		"one point":       map[string]any{"path": [][2]float64{{34, -118}}},
		"bad latitude":    map[string]any{"path": [][2]float64{{95, -118}, {34, -118}}},
		"negative budget": map[string]any{"path": straight(100), "budget_hours": -2},
	} {
		rec := postOvernight(h, body)
		var out map[string]any
		if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out["error"] == nil {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString("{")))
	if rec.Code != 400 {
		t.Errorf("bad json = %d", rec.Code)
	}
	// The listing failing to load is a 503 with a plain message, not a leak.
	down := overnightHandler(hotelArchive{}, func(context.Context) error { return errors.New("pmtiles: http 500 at s3://secret") })
	rec = postOvernight(down, map[string]any{"path": straight(100)})
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "s3://") || strings.Contains(rec.Body.String(), "pmtiles") {
		t.Errorf("archive down = %d %s", rec.Code, rec.Body.String())
	}
	// No archive on this install.
	rec = httptest.NewRecorder()
	handleOvernightCandidates(nil)(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString("{}")))
	if rec.Code != 404 {
		t.Errorf("no archive = %d", rec.Code)
	}
}
