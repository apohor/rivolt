package api

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/ledger"
)

func fp(v float64) *float64 { return &v }

func TestBuildTrips(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	h := func(n float64) time.Time { return t0.Add(time.Duration(n * float64(time.Hour))) }
	home := &ledger.LatLon{Lat: 41.0, Lon: -88.0}
	cfg := ledger.Config{Currency: "USD", HomeRate: 0.15, Home: home}
	drive := func(id string, s, e float64, mi, kwh float64) ledger.Drive {
		return ledger.Drive{ID: id, VehicleID: "v", StartedAt: h(s), EndedAt: h(e), DistanceMi: fp(mi), EnergyKWh: fp(kwh), Source: "live"}
	}
	ds := []ledger.Drive{
		drive("d-commute", -30, -29.5, 10, 3), // lone errand, no trip
		drive("d1", 0, 4, 200, 70),
		drive("d2", 5, 9, 200, 70),
		drive("d3", 20, 24, 400, 130),
	}
	cs := []ledger.Charge{
		{ID: "c-fast", VehicleID: "v", StartedAt: h(4), EndedAt: h(4.5), EnergyKWh: fp(60), PeakKW: fp(150),
			Cost: fp(30), Currency: "USD", CostSource: "network_estimate", Lat: fp(44.5), Lon: fp(-88), Source: "live"},
		{ID: "c-unpriced", VehicleID: "v", StartedAt: h(9.5), EndedAt: h(12), EnergyKWh: fp(30), PeakKW: fp(11),
			Lat: fp(45.5), Lon: fp(-88), Source: "live"},
		{ID: "c-home", VehicleID: "v", StartedAt: h(24.5), EndedAt: h(30), EnergyKWh: fp(50), PeakKW: fp(11),
			Cost: fp(7.5), Currency: "USD", CostSource: "home_rate", Lat: fp(41.0), Lon: fp(-88.0), Source: "live"},
	}
	got := buildTrips(cs, ds, cfg)
	if len(got) != 1 {
		t.Fatalf("want 1 trip, got %d: %+v", len(got), got)
	}
	tr := got[0]
	if tr.Miles != 800 || len(tr.DriveIDs) != 3 || len(tr.ChargeIDs) != 2 {
		t.Fatalf("grouping: %+v", tr)
	}
	if tr.FastSessions != 1 || tr.Cost != 30 || tr.UnpricedSessions != 1 {
		t.Fatalf("money: %+v", tr)
	}
	if tr.EstimatedShare != 1 || tr.CostSourceMix["network_estimate"] != 30 {
		t.Fatalf("mix: %+v", tr)
	}
	if tr.MaxKMFromHome == nil || *tr.MaxKMFromHome < 500 {
		t.Fatalf("max km: %v", tr.MaxKMFromHome)
	}
	if tr.EfficiencyMiPerKWh == nil || tr.Plan != nil {
		t.Fatalf("eff/plan: %+v", tr)
	}
}
