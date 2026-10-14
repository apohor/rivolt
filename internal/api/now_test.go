package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestConnectionState(t *testing.T) {
	cases := []struct {
		replay, paused, reauth bool
		want                   string
	}{
		{false, false, false, "live"},
		{false, false, true, "reauth"},
		{false, true, true, "paused"},
		{true, true, true, "replay"},
	}
	for _, c := range cases {
		if got := connectionState(c.replay, c.paused, c.reauth); got != c.want {
			t.Errorf("%+v = %s, want %s", c, got, c.want)
		}
	}
}

func nowGet(t *testing.T, a nowAPI) (int, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/vehicles/{vehicleID}/now", func(w http.ResponseWriter, req *http.Request) { a.handle(uuid.New(), w, req) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vehicles/rv-1/now", nil))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestNowHandler(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	since := at.Add(-40 * time.Minute)
	soc, peak, tire := 41.0, 148.0, 2.6
	a := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{At: &at, State: "charging", StateSince: &since, SoCAtStateStart: &soc,
				ChargePeakKW: &peak, TirePressureMinB: &tire, FailedSleeps6h: 2}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		now:        func() time.Time { return at },
	}
	code, m := nowGet(t, a)
	if code != 200 || m["state"] != "charging" || m["connection"] != "live" || m["failed_sleeps_6h"] != float64(2) {
		t.Fatalf("%d %v", code, m)
	}
	ch, ok := m["charge"].(map[string]any)
	if !ok || ch["peak_kw"] != 148.0 || ch["soc_start"] != 41.0 {
		t.Fatalf("charge: %v", m["charge"])
	}
	if m["location_fix_at"] != nil {
		t.Fatalf("location_fix_at should be null: %v", m["location_fix_at"])
	}

	// Not charging: no charge object; empty state reads unknown.
	a.snapshot = func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
		return nowSnapshot{}, nil
	}
	_, m = nowGet(t, a)
	if m["charge"] != nil || m["state"] != "unknown" || m["at"] != nil || m["tire_pressure_min_bar"] != nil {
		t.Fatalf("empty: %v", m)
	}
}
