package ledger

import (
	"testing"
	"time"
)

func drv(id, start string, dur time.Duration, mi, kwh float64, fromLat, toLat float64) Drive {
	st := ts(start)
	lon := -97.74
	d := Drive{ID: id, VehicleID: "veh-1", StartedAt: st, EndedAt: st.Add(dur), DistanceMi: f(mi), Source: "live",
		StartLat: f(fromLat), StartLon: f(lon), EndLat: f(toLat), EndLon: f(lon)}
	if kwh > 0 {
		d.EnergyKWh = f(kwh)
	}
	return d
}

const (
	latHome, latStore = 30.27, 30.30 // ~3 km apart
	latA, latB        = 32.07, 33.87 // ~200 and ~400 km north of home
)

func TestBuildTrips(t *testing.T) {
	// Errand: home → store → home. Not a trip.
	errand := []Drive{
		drv("e1", "2026-10-01T15:00:00Z", 15*time.Minute, 2, 0.8, latHome, latStore),
		drv("e2", "2026-10-01T16:00:00Z", 15*time.Minute, 2, 0.8, latStore, latHome),
	}
	// Road trip: home → A (DCFC) → B (L1, unpriced) → home.
	road := []Drive{
		drv("r1", "2026-10-03T08:00:00Z", 2*time.Hour, 125, 50, latHome, latA),
		drv("r2", "2026-10-03T11:00:00Z", 2*time.Hour, 125, 52, latA, latB),
		drv("r3", "2026-10-04T08:00:00Z", 4*time.Hour, 250, 100, latB, latHome),
	}
	dcfc := at(priced(charge("c-dc", "2026-10-03T10:10:00Z", 30*time.Minute, 60, 180), 0.40), latA, -97.74)
	dcfc.CostSource, dcfc.Network = SourceNetworkEstimate, "Electrify America"
	l1 := at(charge("c-l1", "2026-10-03T14:00:00Z", 10*time.Hour, 15, 1.4), latB, -97.74)
	homeCharge := priced(charge("c-home", "2026-10-04T13:00:00Z", 8*time.Hour, 70, 7.6), 0.11)

	in := Input{Drives: append(errand, road...), Charges: []Charge{dcfc, l1, homeCharge}, Config: cfg(),
		Now: ts("2026-10-10T00:00:00Z")}
	res := BuildTrips(in, ts("2026-10-01T00:00:00Z"), ts("2026-10-10T00:00:00Z"), chicago)
	if len(res.Trips) != 1 {
		t.Fatalf("trips = %d, want 1: %+v", len(res.Trips), res.Trips)
	}
	tr := res.Trips[0]
	if tr.TripID != "trip_r1" || tr.Miles != 500 || tr.FastSessions != 1 || len(tr.DriveIDs) != 3 ||
		len(tr.ChargeIDs) != 2 || tr.UnpricedSessions != 1 {
		t.Errorf("trip = %+v", tr)
	}
	if tr.Cost != round4(60*0.40) || tr.CostSourceMix[SourceNetworkEstimate] != tr.Cost ||
		tr.EstimatedShare == nil || *tr.EstimatedShare != 1 {
		t.Errorf("trip money: cost %v mix %v share %v", tr.Cost, tr.CostSourceMix, tr.EstimatedShare)
	}
	if tr.MaxKMFromHome == nil || *tr.MaxKMFromHome < 390 || *tr.MaxKMFromHome > 410 {
		t.Errorf("max_km_from_home = %v, want ~400", tr.MaxKMFromHome)
	}
	if tr.EfficiencyMiKWh == nil || *tr.EfficiencyMiKWh != round4(500.0/202) {
		t.Errorf("efficiency = %v", tr.EfficiencyMiKWh)
	}
	if tr.Plan != nil || !tr.StartedAt.Equal(ts("2026-10-03T08:00:00Z")) || !tr.EndedAt.Equal(ts("2026-10-04T12:00:00Z")) {
		t.Errorf("plan %v span %s–%s", tr.Plan, tr.StartedAt, tr.EndedAt)
	}

	// Window filter: a trip must start in [from, to).
	if res := BuildTrips(in, ts("2026-10-03T09:00:00Z"), ts("2026-10-10T00:00:00Z"), chicago); len(res.Trips) != 0 {
		t.Errorf("trip starting before from was returned")
	}
}

// Without a saved home, only a DC fast charge makes an outing a trip,
// and outings split on long parks.
func TestBuildTrips_NoHome(t *testing.T) {
	c := cfg()
	c.Home = nil
	d1 := drv("d1", "2026-10-03T08:00:00Z", 2*time.Hour, 125, 50, latHome, latA)
	d2 := drv("d2", "2026-10-03T11:00:00Z", 2*time.Hour, 125, 50, latA, latB)
	d3 := drv("d3", "2026-10-05T08:00:00Z", 2*time.Hour, 40, 15, latB, latB) // after a 43 h park
	dc := at(priced(charge("dc", "2026-10-03T10:10:00Z", 30*time.Minute, 60, 180), 0.40), latA, -97.74)
	res := BuildTrips(Input{Drives: []Drive{d1, d2, d3}, Charges: []Charge{dc}, Config: c}, ts("2026-10-01T00:00:00Z"),
		ts("2026-10-10T00:00:00Z"), time.UTC)
	if len(res.Trips) != 1 || res.Trips[0].TripID != "trip_d1" || len(res.Trips[0].DriveIDs) != 2 ||
		res.Trips[0].MaxKMFromHome != nil {
		t.Errorf("trips without home = %+v", res.Trips)
	}
}
