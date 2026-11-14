package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func glanceRequest(t *testing.T, a glanceAPI) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/vehicles/{vehicleID}/glance", func(w http.ResponseWriter, req *http.Request) { a.handle(uuid.New(), w, req) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vehicles/rv-1/glance", nil))
	return rec
}

// The glance answer comes from the stored row alone: glanceAPI holds a row reader, the connection label and a clock, and no Rivian
// client or monitor to call (TestGlanceAPIHoldsNoRivianClient pins that).
func TestGlanceServesTheStoredRow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	soc, mi, target := 71.0, 100.0, 80.0
	var since time.Time
	a := glanceAPI{
		row: func(_ context.Context, _ uuid.UUID, id string, s time.Time) (*glanceResponse, error) {
			since = s
			if id != "rv-1" {
				t.Errorf("vehicle id %q", id)
			}
			return &glanceResponse{At: now.Add(-time.Minute), BatteryLevelPct: &soc, DistanceToEmpty: &mi, ChargeTargetPct: &target, State: "parked"}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		now:        func() time.Time { return now },
	}
	rec := glanceRequest(t, a)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "private, max-age=30" {
		t.Fatalf("status %d, cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	for _, want := range []string{`"version":1`, `"battery_level_pct":71`, `"charge_target_pct":80`, `"connection":"live"`, `"state":"parked"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
	for _, never := range []string{"lat", "lon", "location"} {
		if strings.Contains(body, `"`+never) {
			t.Errorf("body names %s: %s", never, body)
		}
	}
	if !since.Equal(now.Add(-glanceLookback)) {
		t.Errorf("lookback start %v", since)
	}
}

func TestGlanceNoRowAndError(t *testing.T) {
	a := glanceAPI{connection: func(uuid.UUID) string { return "live" }, now: time.Now}
	a.row = func(context.Context, uuid.UUID, string, time.Time) (*glanceResponse, error) { return nil, nil }
	if rec := glanceRequest(t, a); rec.Code != http.StatusNotFound {
		t.Errorf("no row: %d, want 404", rec.Code)
	}
	a.row = func(context.Context, uuid.UUID, string, time.Time) (*glanceResponse, error) {
		return nil, errors.New("db down")
	}
	if rec := glanceRequest(t, a); rec.Code != http.StatusInternalServerError {
		t.Errorf("error: %d, want 500", rec.Code)
	}
}

// A glance refresh must never become a Rivian call, whichever pod answers: nothing the handler holds may be, or take, a type from the
// rivian package (a client, a monitor, a state).
func TestGlanceAPIHoldsNoRivianClient(t *testing.T) {
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		if strings.Contains(rt.PkgPath(), "internal/rivian") {
			t.Errorf("%s is %s: the glance handler must not reach Rivian", path, rt)
		}
		switch rt.Kind() {
		case reflect.Func:
			for i := 0; i < rt.NumIn(); i++ {
				walk(rt.In(i), path+" arg")
			}
			for i := 0; i < rt.NumOut(); i++ {
				walk(rt.Out(i), path+" result")
			}
		case reflect.Pointer, reflect.Slice:
			walk(rt.Elem(), path)
		}
	}
	rt := reflect.TypeOf(glanceAPI{})
	for i := 0; i < rt.NumField(); i++ {
		walk(rt.Field(i).Type, rt.Field(i).Name)
	}
}
