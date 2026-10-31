package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/rivian"
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

// proSurfaceRE names the Pro surfaces that are not built yet (the trip postcard, the long-form year page, widget pushes). A route or
// handler whose name matches must carry the Pro gate, or CI fails: a new Pro surface cannot ship ungated.
var proSurfaceRE = regexp.MustCompile(`(?i)postcard|year[-_]?page|widget`)

// ungatedProSurface reports whether a route (by method, path and handler name) is a Pro surface that is not behind a Pro gate.
func ungatedProSurface(route, handlerName string, gated bool) bool {
	return !gated && (proSurfaceRE.MatchString(route) || proSurfaceRE.MatchString(handlerName))
}

func handlerName(h http.Handler) string {
	if h == nil {
		return ""
	}
	v := reflect.ValueOf(h)
	if v.Kind() == reflect.Func {
		if f := runtime.FuncForPC(v.Pointer()); f != nil {
			return f.Name()
		}
	}
	return reflect.TypeOf(h).String()
}

func TestUnbuiltProSurfacesCannotShipUngated(t *testing.T) {
	// The guard itself: it flags what it should and passes what it should.
	for _, c := range []struct {
		route, handler string
		gated, want    bool
	}{
		{"GET /api/trips/saved/{id}/postcard", "x", false, true},
		{"GET /api/trips/saved/{id}/postcard", "x", true, false},
		{"GET /api/year-page", "x", false, true},
		{"GET /api/year_page/{year}", "x", false, true},
		{"POST /api/push/widget", "x", false, true},
		{"GET /api/year-in-rivolt", "handleYear", false, false}, // the year card is free
		{"GET /api/share/statement", "handleStatementCard", false, false},
		{"GET /api/anything", "api.handleWidgetPush.func1", false, true},
	} {
		if got := ungatedProSurface(c.route, c.handler, c.gated); got != c.want {
			t.Errorf("ungatedProSurface(%q, %q, gated=%v) = %v, want %v", c.route, c.handler, c.gated, got, c.want)
		}
	}
	// Every route of the API.
	err := chi.Walk(contractRouter(t), func(method, route string, h http.Handler, mws ...func(http.Handler) http.Handler) error {
		gated := hasMiddlewareNamed(mws, "proOnlyMiddleware") || hasMiddlewareNamed(mws, "ProMiddleware")
		if ungatedProSurface(method+" "+route, handlerName(h), gated) {
			t.Errorf("%s %s (%s) is a Pro surface (postcard, year page or widget push) without the Pro gate: mount it behind proOnlyMiddleware", method, route, handlerName(h))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type twoVehicles struct{ rivian.Client }

func (twoVehicles) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	return []rivian.Vehicle{{ID: "rv-test-1", Model: "R1T"}, {ID: "rv-test-2", Model: "R1S"}}, nil
}

// The vehicle list always lists every vehicle (management, settings and the vehicle switcher need them all); a free account's extras are
// marked pro_locked. A Pro account, or one with no limit, has no marker.
func TestVehicleListShowsEveryVehicleAndMarksTheExtras(t *testing.T) {
	list := func(only string) map[string]bool {
		rec := httptest.NewRecorder()
		handleVehicles(twoVehicles{}, nil, nil, nil, nil, func(context.Context) string { return only })(rec, httptest.NewRequest(http.MethodGet, "/api/vehicles", nil))
		var vs []struct {
			ID        string `json:"id"`
			ProLocked bool   `json:"pro_locked"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &vs); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%d %s (%v)", rec.Code, rec.Body.String(), err)
		}
		out := map[string]bool{}
		for _, v := range vs {
			out[v.ID] = v.ProLocked
		}
		return out
	}
	if got := list("rv-test-1"); len(got) != 2 || got["rv-test-1"] || !got["rv-test-2"] {
		t.Errorf("free, primary rv-test-1: %v, want both listed, only rv-test-2 locked", got)
	}
	if got := list(""); len(got) != 2 || got["rv-test-1"] || got["rv-test-2"] {
		t.Errorf("no limit: %v, want both listed, none locked", got)
	}
}

// The privacy exits and management paths carry no plan or vehicle gate: every DELETE, the data routes, the export and the vehicle
// settings (profile, lease) work for a hidden vehicle on a free account.
func TestPrivacyAndManagementRoutesCarryNoGate(t *testing.T) {
	gates := []string{"proOnlyMiddleware", "multidayProMiddleware", "freeVehicleMiddleware", "rowVehicleMiddleware", "vehicleGateMiddleware", "historyWindowMiddleware"}
	n := 0
	err := chi.Walk(contractRouter(t), func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		r := strings.TrimSuffix(route, "/")
		private := method == http.MethodDelete || strings.HasPrefix(r, "/api/data/") || r == "/api/export" ||
			strings.HasPrefix(r, "/api/vehicles/{vehicleID}/profile") || strings.HasPrefix(r, "/api/vehicles/{vehicleID}/lease") ||
			r == "/api/vehicles" || r == "/api/vehicles/owned" || r == "/api/settings/primary-vehicle"
		if !private {
			return nil
		}
		n++
		for _, g := range gates {
			if hasMiddlewareNamed(mws, g) {
				t.Errorf("%s %s is a privacy or management route but carries %s", method, route, g)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatalf("only %d routes checked", n)
	}
}

// A free account's statements and totals cover its primary vehicle only: the ledger's vehicle param becomes the primary when none is
// asked for, and another vehicle is Pro. With no limit nothing changes.
func TestLedgerParamsLimitFreeVehicle(t *testing.T) {
	a := ledgerAPI{owns: func(context.Context, uuid.UUID, string) (bool, error) { return true, nil }}
	ask := func(ctx context.Context, q string) (string, int, bool) {
		rec := httptest.NewRecorder()
		p, ok := a.params(rec, httptest.NewRequest(http.MethodGet, "/api/ledger/monthly"+q, nil).WithContext(ctx), uuid.New())
		return p.vehicleID, rec.Code, ok
	}
	free := context.WithValue(context.Background(), freeVehicleKey{}, "rv-test-1")
	if id, _, ok := ask(free, ""); !ok || id != "rv-test-1" {
		t.Errorf("unfiltered: %q %v, want the primary", id, ok)
	}
	if _, code, ok := ask(free, "?vehicle_id=rv-test-2"); ok || code != http.StatusPaymentRequired {
		t.Errorf("another vehicle: %d %v, want 402", code, ok)
	}
	if id, _, ok := ask(context.Background(), ""); !ok || id != "" {
		t.Errorf("no limit: %q %v, want every vehicle", id, ok)
	}
}

// The StoreKit routes exist only where StoreKit is configured: a default router has neither.
func TestAppleBillingRoutesAreOptional(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	has := func(d Deps) map[string]bool {
		out := map[string]bool{}
		_ = chi.Walk(New(d).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(route, "/api/billing/apple") {
				out[method+" "+strings.TrimSuffix(route, "/")] = true
			}
			return nil
		})
		return out
	}
	if got := has(Deps{}); len(got) != 0 {
		t.Errorf("a default router has StoreKit routes: %v", got)
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	got := has(Deps{Auth: svc, AppleIAPNotifications: stub, AppleIAPTransaction: &applebill.Transaction{}})
	if !got["POST /api/billing/apple/notifications"] || !got["POST /api/billing/apple/transaction"] {
		t.Errorf("a configured router lacks them: %v", got)
	}
}
