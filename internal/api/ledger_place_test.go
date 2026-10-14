package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

type fakeRestamper struct {
	gotDry    bool
	gotLat    float64
	gotRadius float64
	calls     int
}

func (f *fakeRestamper) RestampPlace(_ context.Context, lat, _ float64, radius float64, dry bool) (charges.RestampResult, error) {
	f.calls++
	f.gotDry, f.gotLat, f.gotRadius = dry, lat, radius
	return charges.RestampResult{DryRun: dry, Changed: 2, ToHome: 2, Unchanged: 5}, nil
}

func TestRestampPlace(t *testing.T) {
	home := func(set bool) func(context.Context) (settings.HomeLocation, error) {
		return func(context.Context) (settings.HomeLocation, error) {
			return settings.HomeLocation{Set: set, Latitude: 30.27, Longitude: -97.74}, nil
		}
	}
	do := func(h http.HandlerFunc, url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, url, nil))
		return rec
	}

	f := &fakeRestamper{}
	if rec := do(handleRestampPlace(f, home(false)), "/x"); rec.Code != http.StatusConflict || f.calls != 0 {
		t.Errorf("no home = %d (calls %d), want 409 and no write", rec.Code, f.calls)
	}
	rec := do(handleRestampPlace(f, home(true)), "/x?dry_run=1")
	if rec.Code != http.StatusOK || !f.gotDry || f.gotLat != 30.27 || f.gotRadius != rivian.HomeRadiusM ||
		!strings.Contains(rec.Body.String(), `"to_home":2`) || !strings.Contains(rec.Body.String(), `"dry_run":true`) {
		t.Errorf("dry run = %d %s (dry %v lat %v radius %v)", rec.Code, rec.Body.String(), f.gotDry, f.gotLat, f.gotRadius)
	}
	if rec := do(handleRestampPlace(f, home(true)), "/x"); rec.Code != http.StatusOK || f.gotDry {
		t.Errorf("apply = %d, dry %v", rec.Code, f.gotDry)
	}
	if rec := do(handleRestampPlace(nil, home(true)), "/x"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("nil store = %d, want 503", rec.Code)
	}
}
