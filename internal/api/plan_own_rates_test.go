package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/tripadvice"
)

// The planner may use a user's own receipts only where the Money endpoints may: the
// operator flag AND the user's opt-in, asked on every call. With either missing there
// is no figure, whatever rows exist.
func TestReceiptsAllowedNeedsTheFlagAndTheOptIn(t *testing.T) {
	uid := uuid.New()
	if receiptsAllowed(context.Background(), nil, nil, uid) {
		t.Error("allowed with no flag store and no settings")
	}
	if receiptsAllowed(context.Background(), &flags.Store{}, nil, uid) {
		t.Error("allowed with the flag off (a store with no receipts row)")
	}
}

func TestOwnRatesForGivesNothingWhenReceiptsAreNotAllowed(t *testing.T) {
	f := ownRatesFor(Deps{}, uuid.New()) // no database, no flags
	if got := f(context.Background(), "USD", time.Now()); got != nil {
		t.Errorf("own rates without receipts access = %v", got)
	}
}

// The enricher asks the request's own-rates function for the trip's currency and puts
// the answer in the cost context; without the function the context has none.
func TestPlanEnricherTakesOwnRatesFromTheRequestContext(t *testing.T) {
	var asked string
	fn := ownRatesFunc(func(_ context.Context, currency string, _ time.Time) map[string]tripadvice.OwnRate {
		asked = currency
		return map[string]tripadvice.OwnRate{"evgo": {PerKWh: 0.55, Sessions: 4}}
	})
	e := &planEnricher{uid: uuid.New(), packKWh: 100}
	e.load(withPlanOwnRates(context.Background(), fn))
	if r := e.costs.OwnRates["evgo"]; r.PerKWh != 0.55 || r.Sessions != 4 {
		t.Errorf("own rates = %v", e.costs.OwnRates)
	}
	if asked != "USD" {
		t.Errorf("asked for %q, want the default USD", asked)
	}
	e = &planEnricher{uid: uuid.New(), packKWh: 100}
	e.load(context.Background())
	if len(e.costs.OwnRates) != 0 {
		t.Errorf("own rates with no function = %v", e.costs.OwnRates)
	}
}
