package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

type fakeOverrider struct {
	calls    int
	id       string
	place    string
	homeSet  bool
	radius   float64
	recorded bool
	err      error
}

func (f *fakeOverrider) SetPlaceOverride(_ context.Context, id, place string, homeSet bool, _, _, radiusM float64) error {
	f.calls++
	f.id, f.place, f.homeSet, f.radius = id, place, homeSet, radiusM
	return f.err
}

func (f *fakeOverrider) SetLocationOverride(_ context.Context, id string, recorded bool) error {
	f.calls++
	f.id, f.recorded = id, recorded
	return f.err
}

func TestChargeOverrideHandlers(t *testing.T) {
	home := func(context.Context) (settings.HomeLocation, error) {
		return settings.HomeLocation{Set: true, Latitude: 30.27, Longitude: -97.74}, nil
	}
	do := func(h http.HandlerFunc, ctx context.Context, body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Put("/api/charges/{id}/x", h)
		req := httptest.NewRequest(http.MethodPut, "/api/charges/live_c_1/x", strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	bg := context.Background()

	f := &fakeOverrider{}
	if rec := do(handleChargePlacePut(f, home), bg, `{"place":"home"}`); rec.Code != http.StatusNoContent || f.place != "home" || f.id != "live_c_1" {
		t.Errorf("set home = %d %+v", rec.Code, f)
	}
	if rec := do(handleChargePlacePut(f, home), bg, `{"place":null}`); rec.Code != http.StatusNoContent || f.place != "" || !f.homeSet || f.radius != rivian.HomeRadiusM {
		t.Errorf("clear = %d %+v", rec.Code, f)
	}
	for _, body := range []string{`{"place":"garage"}`, `{"place":""}`, `{"place":1}`, `nope`} {
		f = &fakeOverrider{}
		if rec := do(handleChargePlacePut(f, home), bg, body); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Errorf("%s = %d (calls %d), want 400 and no write", body, rec.Code, f.calls)
		}
	}
	f = &fakeOverrider{err: charges.ErrNoSuchCharge}
	if rec := do(handleChargePlacePut(f, home), bg, `{"place":"away"}`); rec.Code != http.StatusNotFound {
		t.Errorf("missing charge = %d, want 404", rec.Code)
	}

	f = &fakeOverrider{}
	if rec := do(handleChargeLocationPut(f), bg, `{"recorded":true}`); rec.Code != http.StatusNoContent || !f.recorded {
		t.Errorf("location = %d %+v", rec.Code, f)
	}
	for _, body := range []string{`{}`, `{"recorded":"yes"}`, `{"recorded":null}`} {
		f = &fakeOverrider{}
		if rec := do(handleChargeLocationPut(f), bg, body); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Errorf("%s = %d (calls %d), want 400 and no write", body, rec.Code, f.calls)
		}
	}

	// View-as never writes.
	viewAs := auth.WithImpersonation(bg, uuid.New(), uuid.New())
	f = &fakeOverrider{}
	if rec := do(handleChargePlacePut(f, home), viewAs, `{"place":"home"}`); rec.Code != http.StatusForbidden || f.calls != 0 {
		t.Errorf("view-as place = %d (calls %d)", rec.Code, f.calls)
	}
	if rec := do(handleChargeLocationPut(f), viewAs, `{"recorded":true}`); rec.Code != http.StatusForbidden || f.calls != 0 {
		t.Errorf("view-as location = %d (calls %d)", rec.Code, f.calls)
	}

	// The iPhone app can't make these writes (web only).
	for _, p := range []string{"PUT /api/charges/x/place", "PUT /api/charges/x/location"} {
		if appWrites[p] {
			t.Errorf("%s is on the app write allowlist", p)
		}
	}
}

// place_auto says what Auto would give, from the current home, next to the user's own place.
func TestChargePlaceAuto(t *testing.T) {
	home := func() (float64, float64, bool) { return 40, -100, true }
	c := charges.Charge{ID: "x", Lat: 40.0001, Lon: -100, PlaceOverride: "away", Place: "away"}
	if got := decorateCharge(c, settings.ChargingConfig{}, home).PlaceAuto; got != "home" {
		t.Errorf("near home = %q, want home", got)
	}
	c.Lat = 40.1
	if got := decorateCharge(c, settings.ChargingConfig{}, home).PlaceAuto; got != "away" {
		t.Errorf("10 km out = %q, want away", got)
	}
	if got := decorateCharge(c, settings.ChargingConfig{}, nil).PlaceAuto; got != "" {
		t.Errorf("no home = %q, want none", got)
	}
}
