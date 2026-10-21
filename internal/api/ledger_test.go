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
	"github.com/apohor/rivolt/internal/offpeak"
)

func ledgerFloat(v float64) *float64 { return &v }

// ledgerRouter mounts the ledger routes the way api.go does, with the
// flag gate and data seams replaced by in-memory fakes.
func ledgerRouter(enabled map[string]bool, la ledgerAPI) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/ledger", func(r chi.Router) {
		r.Use(requireMoneyTabMW(func(uid string) bool { return enabled[uid] }))
		r.Get("/enabled", handleLedgerEnabled(nil))
		r.Get("/monthly", withUser(la.monthly))
		r.Get("/summary", withUser(la.summary))
		r.Get("/trips", withUser(la.trips))
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

func TestLedger_TripsValidation(t *testing.T) {
	uid := uuid.New()
	data := &fakeLedgerData{}
	h := ledgerRouter(map[string]bool{uid.String(): true}, data.api(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)))
	cases := map[string]int{
		"/api/ledger/trips": http.StatusOK,
		"/api/ledger/trips?from=2026-09-01&to=2026-10-01&tz=America/Chicago": http.StatusOK,
		"/api/ledger/trips?from=bad":                                         http.StatusBadRequest,
		"/api/ledger/trips?from=2026-10-01&to=2026-09-01":                    http.StatusBadRequest,
		"/api/ledger/trips?from=2024-01-01&to=2026-01-01":                    http.StatusBadRequest,
	}
	for path, want := range cases {
		if rec := ledgerGET(t, h, uid, path); rec.Code != want {
			t.Errorf("%s = %d, want %d (%s)", path, rec.Code, want, strings.TrimSpace(rec.Body.String()))
		}
	}
	if rec := ledgerGET(t, h, uuid.New(), "/api/ledger/trips"); rec.Code != http.StatusNotFound {
		t.Errorf("flag off = %d, want 404", rec.Code)
	}
	rec := ledgerGET(t, h, uid, "/api/ledger/trips?from=2026-09-01&to=2026-10-01")
	if !strings.Contains(rec.Body.String(), `"trips":[]`) || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Errorf("empty trips body = %s", rec.Body.String())
	}
	// Rows are loaded with padding around the window.
	if !data.gotFrom.Before(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !data.gotTo.After(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("load window %s – %s not padded", data.gotFrom, data.gotTo)
	}
}

func TestLedgerEnabled_ReportsUIThemeNextPerUser(t *testing.T) {
	on, off := uuid.New(), uuid.New()
	flag := func(userID string) bool { return userID == on.String() }
	get := func(h http.HandlerFunc, uid *uuid.UUID) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/api/ledger/enabled", nil)
		if uid != nil {
			req = req.WithContext(auth.WithUser(req.Context(), *uid))
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		var out map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
			t.Fatalf("status %d body %s: %v", rec.Code, rec.Body, err)
		}
		return out
	}
	h := handleLedgerEnabled(flag)
	if got := get(h, &on); !got["enabled"] || !got["ui_theme_next"] {
		t.Errorf("flagged user: %v", got)
	}
	if got := get(h, &off); !got["enabled"] || got["ui_theme_next"] {
		t.Errorf("unflagged user: %v", got)
	}
	// No user in the context, or no flag source: off, never an error.
	if got := get(h, nil); got["ui_theme_next"] {
		t.Errorf("no user: %v", got)
	}
	if got := get(handleLedgerEnabled(nil), &on); !got["enabled"] || got["ui_theme_next"] {
		t.Errorf("nil flag source: %v", got)
	}
	// A nil flags store keeps it off.
	if uiThemeNextEnabled(nil)(on.String()) {
		t.Error("nil store enabled the theme")
	}
}

// The billed_* fields reach /api/ledger/monthly and /summary only for users
// the rivian_receipts flag covers, and use the names the Money UI reads.
func TestLedger_BilledFieldsFollowTheReceiptsFlag(t *testing.T) {
	withReceipts, without := uuid.New(), uuid.New()
	c := ledger.Charge{
		ID: "c1", VehicleID: "veh-1",
		StartedAt: time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 10, 9, 19, 0, 0, 0, time.UTC),
		EnergyKWh: ledgerFloat(40), PeakKW: ledgerFloat(150), FinalState: "charging_complete", Source: "live",
		Cost: ledgerFloat(16), Currency: "USD", PricePerKWh: ledgerFloat(0.4), CostSource: "network_estimate",
		BilledCost: ledgerFloat(14.25), BilledCurrency: "USD", BilledVendor: "Electrify America", BilledCharger: "EA Reno",
	}
	data := &fakeLedgerData{charges: []ledger.Charge{c}}
	la := data.api(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	la.billed = func(uid uuid.UUID) bool { return uid == withReceipts }
	h := ledgerRouter(map[string]bool{withReceipts.String(): true, without.String(): true}, la)

	body := func(uid uuid.UUID, url string) string {
		rec := ledgerGET(t, h, uid, url)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	on := body(withReceipts, "/api/ledger/monthly?month=2026-10")
	for _, key := range []string{`"billed_total":14.25`, `"estimated_total":0`, `"billed_sessions":1`, `"estimated_sessions":0`,
		`"billed_cost":14.25`, `"billed_currency":"USD"`, `"billed_vendor":"Electrify America"`, `"billed_charger":"EA Reno"`} {
		if !strings.Contains(on, key) {
			t.Errorf("monthly with the flag on lacks %s:\n%s", key, on)
		}
	}
	if sum := body(withReceipts, "/api/ledger/summary?months=1"); !strings.Contains(sum, `"billed_total":14.25`) || !strings.Contains(sum, `"billed_sessions":1`) || strings.Contains(sum, `"billed_vendor"`) {
		t.Errorf("summary with the flag on: %s", sum)
	}
	if off := body(without, "/api/ledger/monthly?month=2026-10"); strings.Contains(off, "billed_") || strings.Contains(off, "estimated_total") {
		t.Errorf("monthly with the flag off carries billed fields:\n%s", off)
	}
}

// GET /api/ledger/savings: the card's figure, the counts under it, the months it
// covers, and "needs" instead of a number until the inputs are there.
func TestLedgerSavings(t *testing.T) {
	uid := uuid.New()
	chi, _ := time.LoadLocation("America/Chicago")
	home := &ledger.LatLon{Lat: 30.2701, Lon: -97.7401}
	// Three Wednesday-evening plug-ins in September 2026: 30 kWh over 4 h each,
	// paid at the flat 0.11 = 3.30, plug-in 18:00 to 08:00 so the night window fits.
	mk := func(id string, day int) ledger.Charge {
		st := time.Date(2026, 9, day, 18, 0, 0, 0, chi).UTC()
		kwh, peak, cost, rate, act := 30.0, 7.4, 3.30, 0.11, 4*3600.0
		lat, lon := home.Lat, home.Lon
		return ledger.Charge{ID: id, VehicleID: "veh-1", StartedAt: st, EndedAt: st.Add(14 * time.Hour), CreatedAt: st,
			EnergyKWh: &kwh, PeakKW: &peak, Cost: &cost, PricePerKWh: &rate, Currency: "USD", CostSource: "home_rate",
			ActiveSeconds: &act, FinalState: "charging_complete", Source: "live", Lat: &lat, Lon: &lon}
	}
	f := &fakeLedgerData{charges: []ledger.Charge{mk("a", 2), mk("b", 9), mk("c", 16)}}
	a := f.api(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	cfg := ledger.Config{Currency: "USD", HomeRate: 0.21, Home: home}
	a.config = func(context.Context, uuid.UUID) (ledger.Config, error) { return cfg, nil }
	sch, e := offpeak.Normalize(offpeak.Schedule{TZ: "America/Chicago", Windows: []offpeak.Window{{Days: "weekdays", Start: "21:00", End: "06:00", Rate: 0.09}}})
	if e != nil {
		t.Fatal(e)
	}
	a.schedule = func(context.Context, uuid.UUID) offpeak.Schedule { return sch }
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.savings(uid, w, r) })

	type body struct {
		Months         []string       `json:"months"`
		TZ             string         `json:"tz"`
		Considered     int            `json:"sessions_considered"`
		Moved          int            `json:"sessions_moved"`
		LeftOut        int            `json:"sessions_left_out"`
		Reasons        map[string]int `json:"left_out_reasons"`
		AsCharged      float64        `json:"as_charged_total"`
		IfMoved        float64        `json:"if_moved_total"`
		CouldHaveSaved *float64       `json:"could_have_saved"`
		Needs          string         `json:"needs"`
		Basis          string         `json:"basis"`
	}
	get := func(url string) (int, body) {
		rec := ledgerGET(t, h, uid, url)
		var b body
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return rec.Code, b
	}

	// Default: the last complete month (September, now being October 21), cut in
	// the schedule's zone.
	code, b := get("/api/ledger/savings")
	if code != 200 || len(b.Months) != 1 || b.Months[0] != "2026-09" || b.TZ != "America/Chicago" {
		t.Fatalf("default month: %d %+v", code, b)
	}
	if b.Considered != 3 || b.Moved != 3 || b.AsCharged != 9.90 || b.IfMoved != 8.10 || b.CouldHaveSaved == nil || *b.CouldHaveSaved != 1.80 || b.Needs != "" || b.Basis == "" {
		t.Errorf("figures: %+v", b)
	}
	// And it ties to the Money statement's home AC line for the same month.
	rec := ledgerGET(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.monthly(uid, w, r) }), uid, "/api/ledger/monthly?month=2026-09&tz=America/Chicago")
	var m struct {
		Energy struct {
			ByPlaceKind map[string]map[string]struct {
				Cost float64 `json:"cost"`
			} `json:"by_place_kind"`
		} `json:"energy"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if got := m.Energy.ByPlaceKind["home"]["ac"].Cost; got != b.AsCharged {
		t.Errorf("as charged %v, Money's home AC cost %v", b.AsCharged, got)
	}
	// Several months, an explicit month.
	if code, b = get("/api/ledger/savings?month=2026-09&months=3"); code != 200 || len(b.Months) != 3 || b.Months[0] != "2026-07" || b.Months[2] != "2026-09" {
		t.Errorf("months: %d %v", code, b.Months)
	}
	// Missing inputs: counts but no figure.
	a.schedule = func(context.Context, uuid.UUID) offpeak.Schedule { return offpeak.Schedule{} }
	if _, b = get("/api/ledger/savings?tz=America/Chicago"); b.Needs != "offpeak_schedule" || b.CouldHaveSaved != nil || b.Considered != 3 {
		t.Errorf("no schedule: %+v", b)
	}
	a.schedule = func(context.Context, uuid.UUID) offpeak.Schedule { return sch }
	cfg.HomeRate = 0
	if _, b = get("/api/ledger/savings"); b.Needs != "home_rate" || b.CouldHaveSaved != nil {
		t.Errorf("no home rate: %+v", b)
	}
	cfg.HomeRate = 0.21
	f.charges = f.charges[:2]
	if _, b = get("/api/ledger/savings"); b.Needs != "more_home_sessions" || b.CouldHaveSaved != nil || b.Considered != 2 {
		t.Errorf("two sessions: %+v", b)
	}
	// Bad input.
	for _, u := range []string{"?months=0", "?months=13", "?months=x", "?month=2026-13", "?tz=Mars/Base"} {
		if code, _ := get("/api/ledger/savings" + u); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", u, code)
		}
	}
}
