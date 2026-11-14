package ledger

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/offpeak"
)

// Synthetic sessions only. October 2026, one month.
var coachOct = func() []Period {
	p, _ := ParsePeriod("2026-10", time.UTC)
	return []Period{p}
}()

func homeAC(id, day string, kwh, rate float64, src string) Charge {
	c := priced(charge(id, "2026-10-"+day+"T01:00:00Z", 6*time.Hour, kwh, 7.2), rate)
	c.CostSource = src
	return c
}

func fastAway(id, day string, kwh, rate float64) Charge {
	c := at(priced(charge(id, "2026-10-"+day+"T14:00:00Z", 30*time.Minute, kwh, 150), rate), awayLat, awayLon)
	c.CostSource = SourceObservedEstimate
	return c
}

func driveMi(id, day string, mi float64) Drive {
	return Drive{ID: id, VehicleID: "veh-1", StartedAt: ts("2026-10-" + day + "T12:00:00Z"), EndedAt: ts("2026-10-" + day + "T13:00:00Z"), DistanceMi: f(mi), EnergyKWh: f(mi * 0.4), Source: "live"}
}

func coachInput(cs []Charge, ds []Drive, c Config) Input {
	return Input{Charges: cs, Drives: ds, Config: c, Now: ts("2026-10-28T12:00:00Z")}
}

func TestCoachCostPerKWhSplitsHomeAndFastAndMarksEstimates(t *testing.T) {
	cs := []Charge{homeAC("h1", "02", 30, 0.20, SourceHomeRate), homeAC("h2", "04", 30, 0.20, SourceHomeRate), fastAway("f1", "10", 50, 0.50)}
	got := BuildCoach(coachInput(cs, nil, cfg()), coachOct, offpeak.Schedule{}, true)
	k := got.CostPerKWh
	if k.Needs != "" || k.Home.PerKWh == nil || *k.Home.PerKWh != 0.2 || k.FastCharging.PerKWh == nil || *k.FastCharging.PerKWh != 0.5 {
		t.Fatalf("%+v", k)
	}
	if k.FastPremiumPct == nil || *k.FastPremiumPct != 150 {
		t.Errorf("premium = %v, want 150", k.FastPremiumPct)
	}
	// A rate the user typed is not an estimate; the typical public price is.
	if k.Home.Estimated || !k.FastCharging.Estimated || !k.Estimated || k.FastCharging.EstimatedCostShare != 1 {
		t.Errorf("estimated flags: %+v", k)
	}
	if k.Public.PerKWh != nil || k.Public.Sessions != 0 {
		t.Errorf("fast charging away must not also count as public AC: %+v", k.Public)
	}
}

func TestCoachHomeRateIsEstimatedWhileItIsTheStateAverage(t *testing.T) {
	cs := []Charge{homeAC("h1", "02", 30, 0.20, SourceHomeRate), homeAC("h2", "04", 30, 0.20, SourceHomeRate)}
	c := cfg()
	c.HomeRateEstimated = true
	got := BuildCoach(coachInput(cs, nil, c), coachOct, offpeak.Schedule{}, true)
	if !got.CostPerKWh.Home.Estimated || got.CostPerKWh.Home.EstimatedCostShare != 1 || !got.CostPerKWh.Estimated {
		t.Errorf("state-average home rate not marked estimated: %+v", got.CostPerKWh.Home)
	}
	// A bill is never an estimate, even at home.
	b := homeAC("h3", "06", 30, 0.30, SourceRivian)
	got = BuildCoach(coachInput([]Charge{b, homeAC("h4", "08", 30, 0.30, SourceRivian)}, nil, c), coachOct, offpeak.Schedule{}, true)
	if got.CostPerKWh.Home.Estimated {
		t.Errorf("billed sessions marked estimated: %+v", got.CostPerKWh.Home)
	}
}

func TestCoachCostPer100NeedsEnoughDrivingAndPricedCharging(t *testing.T) {
	cs := []Charge{homeAC("h1", "02", 40, 0.25, SourceHomeRate)}
	// 50 mi is under the minimum distance.
	got := BuildCoach(coachInput(cs, []Drive{driveMi("d1", "03", 50)}, cfg()), coachOct, offpeak.Schedule{}, true)
	if got.CostPer100.Needs != "more_driving" || got.CostPer100.Per100Mi != nil {
		t.Errorf("short: %+v", got.CostPer100)
	}
	// 200 mi, $10 spent: $5.00 per 100 mi.
	got = BuildCoach(coachInput(cs, []Drive{driveMi("d1", "03", 200)}, cfg()), coachOct, offpeak.Schedule{}, true)
	c := got.CostPer100
	if c.Needs != "" || c.Per100Mi == nil || *c.Per100Mi != 5 || c.Per100Km == nil || *c.Per100Km != 3.11 {
		t.Errorf("figure: %+v", c)
	}
	if got.HeadlineID != InsightCostPer100 {
		t.Errorf("headline = %q", got.HeadlineID)
	}
}

func TestCoachWithholdsCostPer100WhenTooMuchEnergyIsUnpriced(t *testing.T) {
	priced1 := homeAC("h1", "02", 30, 0.25, SourceHomeRate)
	unpriced := charge("u1", "2026-10-09T14:00:00Z", 30*time.Minute, 20, 150) // 20 of 50 kWh, 40%, no price
	unpriced = at(unpriced, awayLat, awayLon)
	got := BuildCoach(coachInput([]Charge{priced1, unpriced}, []Drive{driveMi("d1", "03", 300)}, cfg()), coachOct, offpeak.Schedule{}, true)
	c := got.CostPer100
	if c.Needs != "unpriced_charges" || c.Per100Mi != nil || c.UnpricedShare <= MaxUnpricedShare {
		t.Errorf("%+v", c)
	}
	// A small unpriced share still gives a figure.
	small := charge("u2", "2026-10-09T14:00:00Z", 30*time.Minute, 5, 150)
	small = at(small, awayLat, awayLon)
	got = BuildCoach(coachInput([]Charge{priced1, small}, []Drive{driveMi("d1", "03", 300)}, cfg()), coachOct, offpeak.Schedule{}, true)
	if got.CostPer100.Needs != "" || got.CostPer100.Per100Mi == nil {
		t.Errorf("small share: %+v", got.CostPer100)
	}
}

func TestCoachOffpeakShiftIsAlwaysAnEstimateAndFlatRateStopsIt(t *testing.T) {
	got := BuildCoach(coachInput(nil, nil, cfg()), coachOct, nightSched(t), true)
	if got.OffpeakShift.Needs != "flat_rate" || got.OffpeakShift.CouldHaveSaved != nil || !got.OffpeakShift.Estimated {
		t.Errorf("flat: %+v", got.OffpeakShift)
	}
	got = BuildCoach(coachInput(nil, nil, cfg()), coachOct, nightSched(t), false)
	if got.OffpeakShift.Needs == "" || got.OffpeakShift.Needs == "flat_rate" {
		t.Errorf("needs = %q, want the savings reason", got.OffpeakShift.Needs)
	}
	if got.HeadlineID != "" || len(got.Order) != 3 {
		t.Errorf("empty window: headline %q order %v", got.HeadlineID, got.Order)
	}
}

func TestCoachThinDataGivesNeedsNotFigures(t *testing.T) {
	got := BuildCoach(coachInput(nil, nil, cfg()), coachOct, offpeak.Schedule{}, true)
	if got.CostPerKWh.Needs != "priced_charges" || got.CostPer100.Needs == "" || got.HeadlineID != "" {
		t.Errorf("%+v", got)
	}
}

func TestCoachCountsAForeignCurrencyChargeAsUnpriced(t *testing.T) {
	home := homeAC("h1", "02", 30, 0.25, SourceHomeRate) // 30 kWh, $7.50
	cad := fastAway("f1", "10", 30, 0.60)                // 30 of 60 kWh in another currency
	cad.Currency = "CAD"
	got := BuildCoach(coachInput([]Charge{home, cad}, []Drive{driveMi("d1", "03", 300)}, cfg()), coachOct, offpeak.Schedule{}, true)
	c := got.CostPer100
	if c.Needs != "unpriced_charges" || c.Per100Mi != nil || c.UnpricedShare < 0.49 {
		t.Errorf("%+v", c)
	}
}

func TestCoachAllFreeChargingIsAZeroFigureNotMissingPrices(t *testing.T) {
	free := homeAC("h1", "02", 40, 0, SourceUser)
	zero := 0.0
	free.Cost = &zero
	got := BuildCoach(coachInput([]Charge{free}, []Drive{driveMi("d1", "03", 200)}, cfg()), coachOct, offpeak.Schedule{}, true)
	c := got.CostPer100
	if c.Needs != "" || c.Per100Mi == nil || *c.Per100Mi != 0 {
		t.Errorf("%+v", c)
	}
}
