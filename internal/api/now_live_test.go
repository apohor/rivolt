package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/ledger"
)

func TestNowDriveFromRow(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	d := drives.Drive{StartedAt: now.Add(-20 * time.Minute), EndedAt: now.Add(-5 * time.Second), DistanceMi: 10, EnergyUsedKWh: 4, AvgSpeedMph: 31}
	got := nowDriveFromRow(d, now)
	if got == nil || got.DistanceMi != 10 || got.DurationS < 1190 || got.DurationS > 1200 || got.EnergyKWh == nil || *got.EnergyKWh != 4 ||
		got.EfficiencyMiPerKWh == nil || *got.EfficiencyMiPerKWh != 2.5 || got.AvgSpeedMph == nil || *got.AvgSpeedMph != 31 {
		t.Fatalf("%+v", got)
	}
	// Just started: no energy yet, and a few tenths of a mile is too short for an efficiency.
	d = drives.Drive{StartedAt: now.Add(-time.Minute), EndedAt: now, DistanceMi: 0.2, EnergyUsedKWh: 0.05}
	got = nowDriveFromRow(d, now)
	if got == nil || got.EfficiencyMiPerKWh != nil || got.AvgSpeedMph != nil {
		t.Fatalf("short drive: %+v", got)
	}
	d.EnergyUsedKWh = 0
	if got = nowDriveFromRow(d, now); got.EnergyKWh != nil || got.EfficiencyMiPerKWh != nil {
		t.Fatalf("no energy: %+v", got)
	}
	// A row the recorder stopped writing is not the drive in progress.
	d = drives.Drive{StartedAt: now.Add(-3 * time.Hour), EndedAt: now.Add(-2 * time.Hour), DistanceMi: 10, EnergyUsedKWh: 4}
	if nowDriveFromRow(d, now) != nil {
		t.Fatal("a stale row was shown")
	}
}

func TestNowChargeFromResponse(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	mk := func(c charges.Charge, src string) chargeResponse {
		c.EndedAt = now.Add(-10 * time.Second)
		return chargeResponse{Charge: c, Classification: &ledger.Classification{CostSource: src}}
	}
	// Rivian-billed: the stored cost, not an estimate.
	got := nowChargeFromResponse(mk(charges.Charge{EnergyAddedKWh: 12, Cost: 6.5, Currency: "USD"}, "rivian"), now)
	if got.Cost == nil || *got.Cost != 6.5 || got.Currency != "USD" || got.CostSource != "rivian" || got.CostIsEstimate || *got.EnergyAddedKWh != 12 {
		t.Fatalf("rivian: %+v", got)
	}
	// Home rate: the stored snapshot, marked as an estimate.
	got = nowChargeFromResponse(mk(charges.Charge{EnergyAddedKWh: 12, Cost: 1.8, Currency: "USD"}, "home_rate"), now)
	if got.Cost == nil || *got.Cost != 1.8 || got.CostSource != "home_rate" || !got.CostIsEstimate {
		t.Fatalf("home: %+v", got)
	}
	// Unpriced and unknown (a fast charge Rivian has not priced yet): null, never 0.
	got = nowChargeFromResponse(mk(charges.Charge{EnergyAddedKWh: 12}, "unknown"), now)
	if got.Cost != nil || got.CostSource != "" || got.CostIsEstimate || got.EnergyAddedKWh == nil {
		t.Fatalf("unknown: %+v", got)
	}
	// No stored price yet but a home estimate: shown as a home-rate estimate.
	r := mk(charges.Charge{EnergyAddedKWh: 10}, "home_rate")
	r.EstimatedCost, r.EstimatedCurrency = 1.5, "USD"
	if got = nowChargeFromResponse(r, now); got.Cost == nil || *got.Cost != 1.5 || got.Currency != "USD" || !got.CostIsEstimate {
		t.Fatalf("estimate: %+v", got)
	}
	// The first minute of a fast charge (under 1 kWh, so the charge list's fast-charge test says no): no home-rate estimate, whatever the place says.
	yes := true
	for name, c := range map[string]charges.Charge{
		"peak at a fast charger":             {EnergyAddedKWh: 0.4, MaxPowerKW: 148},
		"Rivian charger, no saved home":      {EnergyAddedKWh: 0.4, MaxPowerKW: 20, RivianCharger: &yes},
		"Rivian charger, away from the home": {EnergyAddedKWh: 0.4, MaxPowerKW: 20, RivianCharger: &yes, Place: "away"},
	} {
		for _, auto := range []string{"home", "away", ""} {
			if name != "peak at a fast charger" && auto == "home" && c.Place == "" {
				continue // a Rivian charger inside the home radius is the Wall Charger case below
			}
			r = mk(c, "home_rate")
			r.EstimatedCost, r.EstimatedCurrency, r.PlaceAuto = 1.5, "USD", auto
			if got = nowChargeFromResponse(r, now); got.Cost != nil || got.CostIsEstimate || got.EnergyAddedKWh == nil {
				t.Fatalf("%s (auto place %q): %+v", name, auto, got)
			}
		}
	}
	// A Wall Charger at home (a Rivian charger inside the home radius, AC power) stays priced at the home rate.
	r = mk(charges.Charge{EnergyAddedKWh: 0.4, MaxPowerKW: 11, RivianCharger: &yes}, "home_rate")
	r.EstimatedCost, r.EstimatedCurrency, r.PlaceAuto = 0.1, "USD", "home"
	if got = nowChargeFromResponse(r, now); got.Cost == nil || *got.Cost != 0.1 || !got.CostIsEstimate {
		t.Fatalf("wall charger at home: %+v", got)
	}
	// The user's own free price: a real zero.
	got = nowChargeFromResponse(mk(charges.Charge{EnergyAddedKWh: 10, CostSource: "user", Currency: "USD"}, "user"), now)
	if got.Cost == nil || *got.Cost != 0 || got.CostIsEstimate {
		t.Fatalf("free: %+v", got)
	}
	// No classification (price book unreadable): no cost. Stale row: nothing.
	if got = nowChargeFromResponse(chargeResponse{Charge: charges.Charge{Cost: 3, EndedAt: now}}, now); got.Cost != nil {
		t.Fatalf("unclassified: %+v", got)
	}
	r = mk(charges.Charge{Cost: 3}, "rivian")
	r.Charge.EndedAt = now.Add(-time.Hour)
	if nowChargeFromResponse(r, now) != nil {
		t.Fatal("a stale row was shown")
	}
}

func TestNowHandlerLiveFields(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	since := at.Add(-40 * time.Minute)
	cost, kwh := 4.2, 18.0
	a := nowAPI{
		snapshot: func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
			return nowSnapshot{At: &at, State: "charging", StateSince: &since}, nil
		},
		connection: func(uuid.UUID) string { return "live" },
		liveCharge: func(context.Context, uuid.UUID, string, time.Time) *nowChargeLive {
			return &nowChargeLive{EnergyAddedKWh: &kwh, Cost: &cost, Currency: "USD", CostSource: "home_rate", CostIsEstimate: true}
		},
		liveDrive: func(context.Context, uuid.UUID, string, time.Time) *nowDrive {
			t.Fatal("drive read while charging")
			return nil
		},
		now: func() time.Time { return at },
	}
	_, m := nowGet(t, a)
	ch := m["charge"].(map[string]any)
	if ch["cost"] != 4.2 || ch["currency"] != "USD" || ch["cost_source"] != "home_rate" || ch["cost_is_estimate"] != true || ch["energy_added_kwh"] != 18.0 {
		t.Fatalf("charge: %v", ch)
	}
	if v, ok := m["drive"]; !ok || v != nil {
		t.Fatalf("drive must be present and null while charging: %v (%v)", v, ok)
	}

	// Driving: the drive object; no charge object and no charge read.
	a.snapshot = func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
		return nowSnapshot{At: &at, State: "driving", StateSince: &since}, nil
	}
	a.liveCharge = func(context.Context, uuid.UUID, string, time.Time) *nowChargeLive {
		t.Fatal("charge read while driving")
		return nil
	}
	eff := 2.5
	a.liveDrive = func(context.Context, uuid.UUID, string, time.Time) *nowDrive {
		return &nowDrive{StartedAt: since, DistanceMi: 10, DurationS: 1200, EfficiencyMiPerKWh: &eff}
	}
	_, m = nowGet(t, a)
	dr, ok := m["drive"].(map[string]any)
	if !ok || dr["distance_mi"] != 10.0 || dr["duration_s"] != 1200.0 || dr["efficiency_mi_per_kwh"] != 2.5 || dr["energy_kwh"] != nil || m["charge"] != nil {
		t.Fatalf("drive: %v / charge %v", m["drive"], m["charge"])
	}

	// A store that finds nothing leaves the fields null, not an error.
	a.liveDrive = func(context.Context, uuid.UUID, string, time.Time) *nowDrive { return nil }
	if code, m := nowGet(t, a); code != 200 || m["drive"] != nil {
		t.Fatalf("%d %v", code, m)
	}
}
