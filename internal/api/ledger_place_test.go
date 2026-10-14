package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

type fakeRestamper struct {
	got     charges.RestampOptions
	calls   int
	undoID  *uuid.UUID
	undoErr error
}

func (f *fakeRestamper) RestampPlace(_ context.Context, o charges.RestampOptions) (charges.RestampResult, error) {
	f.calls++
	f.got = o
	return charges.RestampResult{DryRun: o.DryRun, Changed: 2, ToHome: 2, Unchanged: 5,
		WouldPriceAtHomeRate: 1, TotalCostDelta: 1.6, Currency: o.Currency}, nil
}

func (f *fakeRestamper) UndoRestamp(_ context.Context, id *uuid.UUID) (charges.UndoResult, error) {
	f.undoID = id
	if f.undoErr != nil {
		return charges.UndoResult{}, f.undoErr
	}
	return charges.UndoResult{RestoredPlace: 2, Unpriced: 1}, nil
}

func TestRestampPlace(t *testing.T) {
	cfg := func(set bool) restampSettings {
		return restampSettings{
			home: func(context.Context) (settings.HomeLocation, error) {
				return settings.HomeLocation{Set: set, Latitude: 30.27, Longitude: -97.74}, nil
			},
			charging: func(context.Context) (settings.ChargingConfig, error) {
				return settings.ChargingConfig{HomePricePerKWh: 0.16, HomeCurrency: "usd"}, nil
			},
		}
	}
	do := func(h http.HandlerFunc, url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, url, nil))
		return rec
	}

	f := &fakeRestamper{}
	if rec := do(handleRestampPlace(f, cfg(false)), "/x"); rec.Code != http.StatusConflict || f.calls != 0 {
		t.Errorf("no home = %d (calls %d), want 409 and no write", rec.Code, f.calls)
	}
	rec := do(handleRestampPlace(f, cfg(true)), "/x?dry_run=1")
	if rec.Code != http.StatusOK || !f.got.DryRun || f.got.HomeLat != 30.27 || f.got.RadiusM != rivian.HomeRadiusM ||
		f.got.HomeRate != 0.16 || f.got.Currency != "USD" || f.got.IsDCFC == nil ||
		!strings.Contains(rec.Body.String(), `"would_price_at_home_rate":1`) || !strings.Contains(rec.Body.String(), `"total_cost_delta":1.6`) {
		t.Errorf("dry run = %d %s; options %+v", rec.Code, rec.Body.String(), f.got)
	}
	if rec := do(handleRestampPlace(f, cfg(true)), "/x"); rec.Code != http.StatusOK || f.got.DryRun {
		t.Errorf("apply = %d, dry %v", rec.Code, f.got.DryRun)
	}
	if rec := do(handleRestampPlace(nil, cfg(true)), "/x"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("nil store = %d, want 503", rec.Code)
	}
}

func TestUndoRestamp(t *testing.T) {
	do := func(h http.HandlerFunc, url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, url, nil))
		return rec
	}
	f := &fakeRestamper{}
	if rec := do(handleUndoRestamp(f), "/undo"); rec.Code != http.StatusOK || f.undoID != nil ||
		!strings.Contains(rec.Body.String(), `"unpriced":1`) {
		t.Errorf("undo latest = %d %s (id %v)", rec.Code, rec.Body.String(), f.undoID)
	}
	id := uuid.New()
	if rec := do(handleUndoRestamp(f), "/undo?restamp_id="+id.String()); rec.Code != http.StatusOK || f.undoID == nil || *f.undoID != id {
		t.Errorf("undo by id = %d (id %v)", rec.Code, f.undoID)
	}
	if rec := do(handleUndoRestamp(f), "/undo?restamp_id=nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", rec.Code)
	}
	if rec := do(handleUndoRestamp(&fakeRestamper{undoErr: charges.ErrNoRestamp}), "/undo"); rec.Code != http.StatusNotFound {
		t.Errorf("nothing to undo = %d, want 404", rec.Code)
	}
}
