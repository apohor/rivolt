package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/ledger"
)

func ledgerFloat(v float64) *float64 { return &v }

// ledgerRouter mounts the ledger routes the way api.go does, with the
// flag gate and data seams replaced by in-memory fakes.
func ledgerRouter(enabled map[string]bool, la ledgerAPI) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/ledger", func(r chi.Router) {
		r.Use(requireMoneyTabMW(func(uid string) bool { return enabled[uid] }))
		r.Get("/enabled", handleLedgerEnabled)
		r.Get("/monthly", withUser(la.monthly))
		r.Get("/summary", withUser(la.summary))
	})
	return r
}

type fakeLedgerData struct {
	charges  []ledger.Charge
	owned    map[string]bool
	loadErr  error
	gotFrom  time.Time
	gotTo    time.Time
	gotVeh   string
	gotUser  uuid.UUID
	loadHits int
}

func (f *fakeLedgerData) api(now time.Time) ledgerAPI {
	return ledgerAPI{
		load: func(_ context.Context, uid uuid.UUID, vehicleID string, from, to time.Time) ([]ledger.Charge, []ledger.Drive, error) {
			f.loadHits++
			f.gotUser, f.gotVeh, f.gotFrom, f.gotTo = uid, vehicleID, from, to
			return f.charges, nil, f.loadErr
		},
		owns: func(_ context.Context, _ uuid.UUID, vehicleID string) (bool, error) {
			return f.owned[vehicleID], nil
		},
		config: func(context.Context, uuid.UUID) (ledger.Config, error) {
			return ledger.Config{Currency: "USD", HomeRate: 0.11}, nil
		},
		now: func() time.Time { return now },
	}
}

func ledgerGET(t *testing.T, h http.Handler, uid uuid.UUID, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if uid != uuid.Nil {
		req = req.WithContext(auth.WithUser(req.Context(), uid))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLedger_FlagGate(t *testing.T) {
	on, off := uuid.New(), uuid.New()
	data := &fakeLedgerData{}
	h := ledgerRouter(map[string]bool{on.String(): true}, data.api(time.Now()))

	for _, path := range []string{"/api/ledger/enabled", "/api/ledger/monthly?month=2026-10", "/api/ledger/summary"} {
		if rec := ledgerGET(t, h, off, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s with flag off = %d, want 404", path, rec.Code)
		}
		if rec := ledgerGET(t, h, uuid.Nil, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s unauthenticated = %d, want 404", path, rec.Code)
		}
	}
	if data.loadHits != 0 {
		t.Errorf("gated requests reached the loader %d times", data.loadHits)
	}
	rec := ledgerGET(t, h, on, "/api/ledger/enabled")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Errorf("enabled probe = %d %s", rec.Code, rec.Body.String())
	}
}

func TestLedger_MonthlyValidation(t *testing.T) {
	uid := uuid.New()
	data := &fakeLedgerData{owned: map[string]bool{"veh-mine": true}}
	h := ledgerRouter(map[string]bool{uid.String(): true}, data.api(time.Now()))
	cases := map[string]int{
		"/api/ledger/monthly":                                   http.StatusBadRequest,
		"/api/ledger/monthly?month=2026-13":                     http.StatusBadRequest,
		"/api/ledger/monthly?month=2026-10&tz=Mars/Olympus":     http.StatusBadRequest,
		"/api/ledger/monthly?month=2026-10&tz=Local":            http.StatusBadRequest,
		"/api/ledger/monthly?month=2026-10&vehicle_id=veh-not":  http.StatusNotFound,
		"/api/ledger/summary?months=0":                          http.StatusBadRequest,
		"/api/ledger/summary?months=25":                         http.StatusBadRequest,
		"/api/ledger/summary?months=x":                          http.StatusBadRequest,
		"/api/ledger/monthly?month=2026-10&vehicle_id=veh-mine": http.StatusOK,
	}
	for path, want := range cases {
		if rec := ledgerGET(t, h, uid, path); rec.Code != want {
			t.Errorf("%s = %d, want %d (%s)", path, rec.Code, want, strings.TrimSpace(rec.Body.String()))
		}
	}
}

func TestLedger_MonthlyResponse(t *testing.T) {
	uid := uuid.New()
	st := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC) // Sep 30 22:00 in Chicago
	data := &fakeLedgerData{
		owned: map[string]bool{"veh-1": true},
		charges: []ledger.Charge{{
			ID: "c1", VehicleID: "veh-1", StartedAt: st, EndedAt: st.Add(time.Hour),
			EnergyKWh: ledgerFloat(10), Cost: ledgerFloat(1.1), PricePerKWh: ledgerFloat(0.11),
			Currency: "USD", Source: "live", FinalState: "charging_complete",
		}},
	}
	h := ledgerRouter(map[string]bool{uid.String(): true}, data.api(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))

	rec := ledgerGET(t, h, uid, "/api/ledger/monthly?month=2026-09&tz=America/Chicago&vehicle_id=veh-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var m ledger.Monthly
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Period != "2026-09" || m.TZ != "America/Chicago" || m.TZSource != "query" {
		t.Errorf("period/tz = %s %s %s", m.Period, m.TZ, m.TZSource)
	}
	if m.VehicleID == nil || *m.VehicleID != "veh-1" || data.gotVeh != "veh-1" || data.gotUser != uid {
		t.Errorf("vehicle scoping: resp %v, loader veh %q user %s", m.VehicleID, data.gotVeh, data.gotUser)
	}
	if m.Spend.Total != 1.1 || m.Spend.BySource["home_rate"] != 1.1 || len(m.Receipts) != 1 {
		t.Errorf("spend %+v receipts %d", m.Spend, len(m.Receipts))
	}
	// The loader window is padded around the Chicago month.
	if !data.gotFrom.Before(time.Date(2026, 9, 1, 5, 0, 0, 0, time.UTC)) ||
		!data.gotTo.After(time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("load window %s – %s does not cover the month", data.gotFrom, data.gotTo)
	}

	// Without tz the month is UTC, so the same session lands in October.
	rec = ledgerGET(t, h, uid, "/api/ledger/monthly?month=2026-10")
	var utc ledger.Monthly
	_ = json.Unmarshal(rec.Body.Bytes(), &utc)
	if utc.TZSource != "default_utc" || utc.Energy.KWhAdded != 10 || utc.VehicleID != nil {
		t.Errorf("UTC default: tz_source %s kwh %v vehicle %v", utc.TZSource, utc.Energy.KWhAdded, utc.VehicleID)
	}
}

func TestLedger_Summary(t *testing.T) {
	uid := uuid.New()
	data := &fakeLedgerData{}
	h := ledgerRouter(map[string]bool{uid.String(): true}, data.api(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))
	rec := ledgerGET(t, h, uid, "/api/ledger/summary?months=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"receipts"`) {
		t.Errorf("summary must not include receipts: %s", rec.Body.String())
	}
	var out ledgerSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var months []string
	for _, m := range out.Months {
		months = append(months, m.Period)
	}
	if strings.Join(months, ",") != "2026-08,2026-09,2026-10" || data.loadHits != 1 {
		t.Errorf("months = %v (loads %d), want 2026-08..2026-10 in one load", months, data.loadHits)
	}
}

func TestLedger_LoadErrorIs500(t *testing.T) {
	uid := uuid.New()
	data := &fakeLedgerData{loadErr: errors.New("db down")}
	h := ledgerRouter(map[string]bool{uid.String(): true}, data.api(time.Now()))
	if rec := ledgerGET(t, h, uid, "/api/ledger/monthly?month=2026-10"); rec.Code != http.StatusInternalServerError {
		t.Errorf("load error = %d, want 500", rec.Code)
	}
}

func TestLedger_MoneyTabPutValidates(t *testing.T) {
	h := handleFlagsMoneyTabPut(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"users":["x"]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("nil store = %d, want 503", rec.Code)
	}
}
