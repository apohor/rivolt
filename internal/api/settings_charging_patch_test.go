package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
)

// The strict body: known fields only, one object, a rate from 0 to 5 and a currency
// from the offered set, at least one field. Checked before the store is touched (the
// handler here has none: a good body reaches the 503).
func TestChargingRatePatchBody(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"rate", `{"home_price_per_kwh": 0.13}`, 503},
		{"rate zero", `{"home_price_per_kwh": 0}`, 503},
		{"rate at the cap", `{"home_price_per_kwh": 5}`, 503},
		{"currency", `{"home_currency": "EUR"}`, 503},
		{"currency lower case", `{"home_currency": "eur"}`, 503},
		{"both", `{"home_price_per_kwh": 0.31, "home_currency": "CAD"}`, 503},
		{"empty object", `{}`, 400},
		{"nulls only", `{"home_price_per_kwh": null, "home_currency": null}`, 400},
		{"empty body", ``, 400},
		{"not json", `rate=1`, 400},
		{"array", `[{"home_price_per_kwh": 1}]`, 400},
		{"over the cap", `{"home_price_per_kwh": 5.01}`, 400},
		{"negative", `{"home_price_per_kwh": -0.01}`, 400},
		{"huge", `{"home_price_per_kwh": 1e999}`, 400},
		{"rate as a string", `{"home_price_per_kwh": "0.13"}`, 400},
		{"NaN", `{"home_price_per_kwh": NaN}`, 400},
		{"unknown currency", `{"home_currency": "XXX"}`, 400},
		{"empty currency", `{"home_currency": ""}`, 400},
		{"currency as a number", `{"home_currency": 840}`, 400},
		{"unknown field", `{"home_price_per_kwh": 0.13, "flat_rate": true}`, 400},
		{"gas price over the cap", `{"gas_price_per_gallon": 20.01}`, 400},
		{"negative gas price", `{"gas_price_per_gallon": -1}`, 400},
		{"gas price as a string", `{"gas_price_per_gallon": "4"}`, 400},
		{"mpg under the floor", `{"comparison_mpg": 0.99}`, 400},
		{"mpg over the cap", `{"comparison_mpg": 200.01}`, 400},
		{"mpg huge", `{"comparison_mpg": 1e999}`, 400},
		{"mpg null only", `{"comparison_mpg": null}`, 400},
		{"case-different field name", `{"HomePricePerKWh": 0.13}`, 400},
		{"trailing object", `{"home_price_per_kwh": 0.13}{"home_currency": "USD"}`, 400},
		{"trailing junk", `{"home_price_per_kwh": 0.13} x`, 400},
		{"too big", `{"home_price_per_kwh": 0.13, "pad": "` + strings.Repeat("x", 5000) + `"}`, 400},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/settings/charging", strings.NewReader(c.body))
		handleChargingSettingsPatch(nil)(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d %q, want %d", c.name, rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
		}
	}
}

// Through the real router with full URLs, as an iPhone session: the PATCH is let through
// and validated, the web's full-overwrite PUT and "apply my rate to past sessions" are
// not the app's, a browser session is not held to the list, signed-out callers are
// refused, and writes are limited to 30 a minute per user.
func TestChargingRatePatchThroughTheRouter(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc, AuthEnforced: true})
	uid := uuid.New()
	send := func(kind, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader(body))
		req.RemoteAddr = "203.0.113.9:4000"
		ctx := req.Context()
		if kind != "none" {
			ctx = auth.WithUser(ctx, uid)
			ctx = auth.WithSession(ctx, auth.SessionRef{ID: uuid.New(), Kind: kind})
		}
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	const good = `{"home_price_per_kwh": 0.13, "home_currency": "USD"}`
	// The app: the PATCH gets through the list; with no settings store in this router it
	// ends at the store check, and a bad body is a 400.
	if rec := send(sessions.KindApp, "PATCH", "/api/settings/charging", good); denied(rec) || rec.Code != http.StatusServiceUnavailable {
		t.Errorf("app PATCH: %d %q, want it past the list (503 without a store)", rec.Code, rec.Body.String())
	}
	if rec := send(sessions.KindApp, "PATCH", "/api/settings/charging", `{"home_price_per_kwh": 9}`); rec.Code != http.StatusBadRequest {
		t.Errorf("app PATCH over the cap: %d, want 400", rec.Code)
	}
	for _, zero := range []string{`{"comparison_mpg": 0}`, `{"gas_price_per_gallon": 0}`} {
		if rec := send(sessions.KindApp, "PATCH", "/api/settings/charging", zero); denied(rec) || rec.Code != http.StatusServiceUnavailable {
			t.Errorf("app PATCH %s: %d, want it past validation (503 without a store)", zero, rec.Code)
		}
	}
	if rec := send(sessions.KindApp, "PATCH", "/api/settings/charging", `{"flat_rate": true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("app PATCH of another field: %d, want 400", rec.Code)
	}
	// Not the app's: the full overwrite, applying a rate to past charges, one charge's price, the flat-rate switch and the planner
	// favorites. (The price book, its reset, the off-peak schedule and the planner defaults are the app's since Oct 26.)
	for _, c := range [][2]string{
		{"PUT", "/api/settings/charging"},
		{"POST", "/api/charges/apply-rate"},
		{"PATCH", "/api/charges/" + uuid.NewString() + "/pricing"},
		{"PUT", "/api/settings/planner/favorites"},
		{"POST", "/api/settings/charging/networks"},
		{"DELETE", "/api/settings/charging/networks"},
		{"PATCH", "/api/settings/charging/offpeak"},
		{"PUT", "/api/settings/charging/offpeak/"},
	} {
		if rec := send(sessions.KindApp, c[0], c[1], good); !denied(rec) {
			t.Errorf("app %s %s: %d, want the app-write refusal", c[0], c[1], rec.Code)
		}
	}
	// The app's: each gets past the list (what it answers next depends on the body and the missing store, never the refusal).
	for _, c := range [][2]string{
		{"PUT", "/api/settings/charging/offpeak"},
		{"PUT", "/api/settings/charging/flat-rate"},
		{"PUT", "/api/settings/charging/networks"},
		{"POST", "/api/settings/charging/networks/reset"},
		{"PUT", "/api/settings/planner"},
	} {
		if rec := send(sessions.KindApp, c[0], c[1], `{}`); denied(rec) {
			t.Errorf("app %s %s was refused by the write list", c[0], c[1])
		}
	}
	// A browser session may use the PATCH too; a signed-out caller may not.
	if rec := send(sessions.KindWeb, "PATCH", "/api/settings/charging", good); denied(rec) || rec.Code != http.StatusServiceUnavailable {
		t.Errorf("web PATCH: %d", rec.Code)
	}
	if rec := send("none", "PATCH", "/api/settings/charging", good); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed-out PATCH: %d, want 401", rec.Code)
	}
	// 30 a minute per user: the rest of a burst is a 429 with a Retry-After, and a GET is
	// not counted.
	limited := 0
	for i := 0; i < 40; i++ {
		rec := send(sessions.KindApp, "PATCH", "/api/settings/charging", good)
		if rec.Code == http.StatusTooManyRequests {
			limited++
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		}
	}
	if limited < 5 {
		t.Errorf("%d of 40 writes limited, want most of the ones past 30", limited)
	}
	if rec := send(sessions.KindApp, "GET", "/api/settings/charging", ""); rec.Code == http.StatusTooManyRequests {
		t.Error("a GET was counted against the write limit")
	}
}
