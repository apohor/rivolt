package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/sessions"
)

// flatRateAPI is the savings fixture of TestLedgerSavings (three Wednesday-evening home
// sessions, a weekday-night window below the flat rate), with the flat-rate choice set as asked.
func flatRateAPI(t *testing.T, uid uuid.UUID, flat bool) ledgerAPI {
	t.Helper()
	chi, _ := time.LoadLocation("America/Chicago")
	home := &ledger.LatLon{Lat: 30.2701, Lon: -97.7401}
	mk := func(id string, day int) ledger.Charge {
		st := time.Date(2026, 9, day, 18, 0, 0, 0, chi).UTC()
		kwh, peak, cost, rate, act := 30.0, 7.4, 6.30, 0.21, 4*3600.0
		lat, lon := home.Lat, home.Lon
		return ledger.Charge{ID: id, VehicleID: "veh-1", StartedAt: st, EndedAt: st.Add(14 * time.Hour), CreatedAt: st,
			EnergyKWh: &kwh, PeakKW: &peak, Cost: &cost, PricePerKWh: &rate, Currency: "USD", CostSource: "home_rate",
			ActiveSeconds: &act, FinalState: "charging_complete", Source: "live", Lat: &lat, Lon: &lon}
	}
	f := &fakeLedgerData{charges: []ledger.Charge{mk("a", 2), mk("b", 9), mk("c", 16)}}
	a := f.api(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	a.config = func(context.Context, uuid.UUID) (ledger.Config, error) {
		return ledger.Config{Currency: "USD", HomeRate: 0.21, Home: home}, nil
	}
	sch, e := offpeak.Normalize(offpeak.Schedule{TZ: "America/Chicago", Windows: []offpeak.Window{{Days: "weekdays", Start: "21:00", End: "06:00", Rate: 0.09}}})
	if e != nil {
		t.Fatal(e)
	}
	a.schedule = func(context.Context, uuid.UUID) offpeak.Schedule { return sch }
	a.flatRate = func(context.Context, uuid.UUID) bool { return flat }
	return a
}

// Consumer 1, the savings card and the share card's off-peak line (both read this response): a
// flat-rate user gets the reason and no estimate field at all, so a client holding an older
// answer can't keep showing a figure. The same data with the choice off still gets the estimate.
func TestFlatRateSavingsCarriesNoEstimate(t *testing.T) {
	uid := uuid.New()
	get := func(flat bool) map[string]any {
		a := flatRateAPI(t, uid, flat)
		rec := ledgerGET(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.savings(uid, w, r) }), uid, "/api/ledger/savings?month=2026-09")
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	off := get(false)
	if off["could_have_saved"] == nil || off["offpeak_share_pct"] == nil && off["sessions_considered"] == nil {
		t.Fatalf("with the choice off the estimate is there: %v", off)
	}
	flat := get(true)
	if flat["reason"] != "flat_rate" || flat["flat_rate"] != true || flat["basis"] == "" {
		t.Errorf("flat response: %v", flat)
	}
	for k := range flat {
		switch k {
		case "version", "flat_rate", "reason", "basis":
		default:
			t.Errorf("a flat-rate response carries %q; it may hold only the reason", k)
		}
	}
}

// Consumer 2, the statement email: no estimate, so no saving line.
func TestFlatRateStatementEmailHasNoSavingLine(t *testing.T) {
	uid := uuid.New()
	chi, _ := time.LoadLocation("America/Chicago")
	got, err := newStatementSavings(flatRateAPI(t, uid, true))(context.Background(), uid, "2026-09", chi)
	if err != nil || got != nil {
		t.Errorf("flat rate: %+v %v, want no estimate", got, err)
	}
	got, err = newStatementSavings(flatRateAPI(t, uid, false))(context.Background(), uid, "2026-09", chi)
	if err != nil || got == nil || got.CouldHaveSaved == nil {
		t.Errorf("choice off: %+v %v, want the estimate", got, err)
	}
}

func putFlatRate(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handleFlatRatePut(nil)(rec, httptest.NewRequest(http.MethodPut, "/api/settings/charging/flat-rate", strings.NewReader(body)))
	return rec
}

// The write is strict: one object, one bool field, nothing else; a valid body meets the store
// only after that (nil store: 503).
func TestFlatRatePutIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"flat_rate":true,"x":1}`,
		"missing":       `{}`,
		"not a bool":    `{"flat_rate":"yes"}`,
		"null":          `{"flat_rate":null}`,
		"trailing":      `{"flat_rate":true} {"flat_rate":false}`,
		"not json":      `{`,
		"too big":       `{"flat_rate":true,"x":"` + strings.Repeat("a", 2000) + `"}`,
	} {
		if rec := putFlatRate(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	for _, body := range []string{`{"flat_rate":true}`, `{"flat_rate":false}`} {
		if rec := putFlatRate(body); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with no store = %d, want 503", body, rec.Code)
		}
	}
}

// The iPhone may set it, through its own route only (DA, Oct 26: a bool that changes estimates, and a phone-only owner could not
// switch back otherwise). The app's settings PATCH still refuses the field by name; an app session gets through the write list to
// the PUT (the strict body and the store check are the handler's, tested above); other methods on the route stay refused.
func TestFlatRateIsWritableFromTheAppOnlyThroughItsOwnRoute(t *testing.T) {
	rec := httptest.NewRecorder()
	handleChargingSettingsPatch(nil)(rec, httptest.NewRequest(http.MethodPatch, "/api/settings/charging", strings.NewReader(`{"flat_rate":true}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "flat_rate") {
		t.Errorf("PATCH with flat_rate: %d %q, want 400 naming flat_rate", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleChargingSettingsPatch(nil)(rec, httptest.NewRequest(http.MethodPatch, "/api/settings/charging", strings.NewReader(`{"home_price_per_kwh":0.2,"flat_rate":true}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "flat_rate") {
		t.Errorf("PATCH with the rate and flat_rate: %d %q, want 400 naming flat_rate", rec.Code, rec.Body.String())
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec = httptest.NewRecorder()
	asSession(appWriteAllowlistMW(ok), sessions.KindApp).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings/charging/flat-rate", strings.NewReader(`{"flat_rate":true}`)))
	if denied(rec) || rec.Code != http.StatusNoContent {
		t.Errorf("an app session was refused the flat-rate PUT: %d", rec.Code)
	}
	for _, m := range []string{http.MethodPatch, http.MethodPost, http.MethodDelete} {
		rec = httptest.NewRecorder()
		asSession(appWriteAllowlistMW(ok), sessions.KindApp).ServeHTTP(rec, httptest.NewRequest(m, "/api/settings/charging/flat-rate", strings.NewReader(`{"flat_rate":true}`)))
		if !denied(rec) {
			t.Errorf("an app session reached %s on the flat-rate route: %d", m, rec.Code)
		}
	}
}

// The key is exported with the rest of the charging settings.
func TestFlatRateIsExported(t *testing.T) {
	if _, ok := exportSettings(map[string]string{"charging.flat_rate": "1"})["charging.flat_rate"]; !ok {
		t.Error("charging.flat_rate is not in the settings export")
	}
}

// The iPhone's fixture for a flat-rate answer is exactly what the server sends.
func TestFlatRateSavingsMatchesTheIOSFixture(t *testing.T) {
	want, err := os.ReadFile("../../ios/RivoltTests/Fixtures/ledger_savings_flat.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(savingsFlatResponse{Version: ledger.Version, FlatRate: true, Reason: "flat_rate", Basis: savingsFlatBasis})
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	if json.Unmarshal(want, &a) != nil || json.Unmarshal(got, &b) != nil {
		t.Fatal("not json")
	}
	if len(a) != len(b) {
		t.Fatalf("fixture %v, server %v", a, b)
	}
	for k, v := range b {
		if a[k] != v {
			t.Errorf("%s: fixture %v, server %v", k, a[k], v)
		}
	}
}
