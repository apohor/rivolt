package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/entitlements"
)

func TestMultidayNeedsPro(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"one day, no overnight":         {`{"origin":{"lat":1,"lon":2},"days":[{"end":{"lat":3,"lon":4}}]}`, false},
		"one day, null overnight":       {`{"days":[{"end":{},"overnight":null}]}`, false},
		"two days":                      {`{"days":[{"end":{}},{"end":{}}]}`, true},
		"one day with an overnight":     {`{"days":[{"end":{},"overnight":{"l2_kw":7.2}}]}`, true},
		"legacy overnights":             {`{"destination":{"lat":1,"lon":2},"overnights":[{"lat":1,"lon":2}]}`, true},
		"legacy without overnights":     {`{"destination":{"lat":1,"lon":2},"overnights":[]}`, false},
		"re-plan from here":             {`{"origin_from_vehicle":true,"days":[{"end":{}}]}`, true},
		"re-plan from here explicit no": {`{"origin_from_vehicle":false,"days":[{"end":{}}]}`, false},
		"not json (the handler's 400)":  {`{`, false},
		"empty":                         {``, false},
	} {
		if got := multidayNeedsPro([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// With billing off (no service, or a service with billing off) the multi-day planner is open and the handler still reads the whole body.
func TestMultidayMiddlewareFailsOpen(t *testing.T) {
	body := `{"days":[{"end":{}},{"end":{}}],"origin_from_vehicle":true}`
	for name, e := range map[string]*entitlements.Service{"no service": nil, "billing off": entitlements.New(nil, entitlements.Config{})} {
		var got string
		h := multidayProMiddleware(e)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
		}))
		req := httptest.NewRequest(http.MethodPost, "/api/trips/plan-multiday", strings.NewReader(body)).WithContext(auth.WithUser(context.Background(), uuid.New()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || got != body {
			t.Errorf("%s: %d, body seen %q", name, rec.Code, got)
		}
	}
}

// The gate is on the routes that carry it: a route that loses its middleware fails here, not in production.
func TestProGateRoutesCarryTheirMiddleware(t *testing.T) {
	want := map[string]string{
		"POST /api/trips/plan-multiday":     "multidayProMiddleware",
		"GET /api/trips/saved/{id}/actual":  "proOnlyMiddleware",
		"GET /api/trips/saved/{id}/active":  "proOnlyMiddleware",
		"POST /api/trips/saved/{id}/link":   "proOnlyMiddleware",
		"POST /api/trips/saved/{id}/unlink": "proOnlyMiddleware",
		"POST /api/push/apns/activity":      "proOnlyMiddleware",
		"POST /api/push/apns/activity/test": "proOnlyMiddleware",
		"GET /api/drives":                   "freeVehicleMiddleware",
		"GET /api/charges":                  "freeVehicleMiddleware",
		"GET /api/chart-points":             "freeVehicleMiddleware",
		"GET /api/drives/{id}":              "rowVehicleMiddleware",
		"GET /api/charges/{id}":             "rowVehicleMiddleware",
		"GET /api/vehicles/{vehicleID}/now": "vehicleGateMiddleware",
		"GET /api/state/{vehicleID}":        "vehicleGateMiddleware",
		"GET /api/live-drive/{vehicleID}":   "vehicleGateMiddleware",
	}
	found := map[string]bool{}
	err := chi.Walk(contractRouter(t), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		key := method + " " + strings.TrimSuffix(route, "/")
		if name, ok := want[key]; ok {
			found[key] = true
			if !hasMiddlewareNamed(mws, name) {
				t.Errorf("%s is not behind %s", key, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range want {
		if !found[key] {
			t.Errorf("%s is not a route of the API", key)
		}
	}
}

// The free account's vehicle limits the drive and charge lists to it, and another vehicle is Pro; with no limit nothing changes.
func TestVehicleFilterFreeVehicle(t *testing.T) {
	ask := func(ctx context.Context, q string) (string, int, bool) {
		rec := httptest.NewRecorder()
		id, ok := vehicleFilter(rec, httptest.NewRequest(http.MethodGet, "/api/drives"+q, nil).WithContext(ctx))
		return id, rec.Code, ok
	}
	free := context.WithValue(context.Background(), freeVehicleKey{}, "rv-test-1")
	if id, _, ok := ask(free, ""); !ok || id != "rv-test-1" {
		t.Errorf("unfiltered list: %q %v, want the free vehicle", id, ok)
	}
	if id, _, ok := ask(free, "?vehicle_id=rv-test-1"); !ok || id != "rv-test-1" {
		t.Errorf("its own vehicle: %q %v", id, ok)
	}
	if _, code, ok := ask(free, "?vehicle_id=rv-test-2"); ok || code != http.StatusPaymentRequired {
		t.Errorf("another vehicle: %d %v, want 402", code, ok)
	}
	if id, _, ok := ask(context.Background(), ""); !ok || id != "" {
		t.Errorf("no limit: %q %v", id, ok)
	}
	if id, _, ok := ask(context.Background(), "?vehicle_id=rv-test-2"); !ok || id != "rv-test-2" {
		t.Errorf("no limit, filtered: %q %v", id, ok)
	}
}

func TestVehicleGatesFailOpenWithoutAPlan(t *testing.T) {
	pv := &proVehicles{}
	if got := pv.freeVehicle(context.Background(), uuid.New()); got != "" {
		t.Errorf("no database: %q, want no limit", got)
	}
	called := false
	h := vehicleGateMiddleware(pv)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/state/rv-test-1", nil).WithContext(auth.WithUser(context.Background(), uuid.New())))
	if !called {
		t.Error("the gate stopped a request it had no plan for")
	}
}
