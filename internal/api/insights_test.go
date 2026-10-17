package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/insights"
	"github.com/apohor/rivolt/internal/samples"
)

type fakeConditions struct {
	rows         []samples.DriveCondition
	err          error
	vehicle      string
	since, until time.Time
}

func (f *fakeConditions) DriveConditions(_ context.Context, vehicleID string, since, until time.Time) ([]samples.DriveCondition, error) {
	f.vehicle, f.since, f.until = vehicleID, since, until
	return f.rows, f.err
}

func TestEfficiencyInsightsHandler(t *testing.T) {
	temp := 5.0
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	f := &fakeConditions{rows: []samples.DriveCondition{{ID: "a", VehicleID: "v", StartedAt: start, EndedAt: start.Add(time.Hour),
		Source: "live", DistanceMi: 50, EnergyKWh: 20, AvgSpeedMph: 50, OutsideTempC: &temp, DriveMode: "everyday"}}}
	get := func(q string) (*httptest.ResponseRecorder, insights.Efficiency) {
		t.Helper()
		w := httptest.NewRecorder()
		handleEfficiencyInsights(f, nil)(w, httptest.NewRequest(http.MethodGet, "/api/insights/efficiency"+q, nil))
		var e insights.Efficiency
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
				t.Fatal(err)
			}
		}
		return w, e
	}

	w, e := get("?since=2026-10-01T00:00:00Z&until=2026-11-01T00:00:00Z&vehicle=v&units=metric")
	if w.Code != http.StatusOK || e.Units != insights.Metric || e.Total.Drives != 1 || e.Total.Distance != 80.5 {
		t.Fatalf("%d %+v", w.Code, e.Total)
	}
	if f.vehicle != "v" || !f.since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !f.until.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("store got %q %v %v", f.vehicle, f.since, f.until)
	}

	// Defaults: imperial, the 30 days before now.
	if w, e := get(""); w.Code != http.StatusOK || e.Units != insights.Imperial || f.until.Sub(f.since) != 30*24*time.Hour ||
		time.Since(f.until) > time.Minute {
		t.Errorf("defaults: %d %s %v..%v", w.Code, e.Units, f.since, f.until)
	}

	for _, q := range []string{"?since=yesterday", "?until=2026-13-01", "?since=2026-11-01T00:00:00Z&until=2026-10-01T00:00:00Z"} {
		if w, _ := get(q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, w.Code)
		}
	}

	f.err = errors.New(`pq: relation "secret_table" does not exist`)
	if w, _ := get(""); w.Code != http.StatusInternalServerError || w.Body.String() != "efficiency query failed\n" {
		t.Errorf("store error: %d %q (must not leak the error)", w.Code, w.Body.String())
	}

	// No store: an empty breakdown, not an error.
	w = httptest.NewRecorder()
	handleEfficiencyInsights(nil, nil)(w, httptest.NewRequest(http.MethodGet, "/api/insights/efficiency", nil))
	if w.Code != http.StatusOK {
		t.Errorf("no store: %d", w.Code)
	}
}
