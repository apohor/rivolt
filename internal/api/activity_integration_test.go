//go:build integration

package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

// /api/activity on one fixture month: the web's rows and figures. A round trip is merged
// as lib/drives.ts does (legs, driving seconds, summed distance, energy and cost),
// re-parking and short drives are left out, charges carry the ledger's classification
// with the cluster label deciding kind/place where the ledger cannot (three slow
// charges at one spot are home by their cluster alone, a lone slow one is away, a
// 150 kW one is dcfc), and the payload has no coordinates, polyline, place name or
// cluster geometry.
func TestActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "act-a")
	b, _ := db.EnsureUser(ctx, pool, "act-b")
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	home := [2]float64{37.7749, -122.4194}
	dest := [2]float64{37.8044, -122.2712}
	drive := func(uid uuid.UUID, id, vid string, at time.Time, dur time.Duration, miles, s, e, kwh float64, from, to [2]float64) {
		t.Helper()
		if err := df.For(uid).Upsert(ctx, drives.Drive{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(dur),
			StartSoCPct: s, EndSoCPct: e, DistanceMi: miles, EnergyUsedKWh: kwh, MaxSpeedMph: 60, AvgSpeedMph: 30, Source: "live",
			StartLat: from[0], StartLon: from[1], EndLat: to[0], EndLon: to[1], RoutePolyline: "_p~iF~ps|U_ulLnnqC"}); err != nil {
			t.Fatal(err)
		}
	}
	charge := func(uid uuid.UUID, id, vid string, at time.Time, kwh, peak, lat, lon float64) {
		t.Helper()
		if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(40 * time.Minute),
			StartSoCPct: 30, EndSoCPct: 60, EnergyAddedKWh: kwh, MaxPowerKW: peak, Lat: lat, Lon: lon,
			Cost: kwh * 0.4, Currency: "USD", PricePerKWh: 0.4, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	// Charges first (the drives' cost rate comes from the charge before them).
	charge(a, "act-h1", "act-rv-1", base, 10, 11, 30.2, -97.7)
	charge(a, "act-h2", "act-rv-1", base.Add(time.Hour), 10, 11, 30.2, -97.7)
	charge(a, "act-h3", "act-rv-1", base.Add(2*time.Hour), 10, 11, 30.2001, -97.7001)
	charge(a, "act-p1", "act-rv-1", base.Add(3*time.Hour), 5, 7, 40.0, -100.0)
	charge(a, "act-f1", "act-rv-1", base.Add(4*time.Hour), 30, 150, 35.0, -90.0)
	charge(b, "act-b1", "act-rv-b", base, 99, 11, 30.2, -97.7)
	t0 := base.Add(24 * time.Hour)
	drive(a, "act-d1", "act-rv-1", t0, 20*time.Minute, 5, 80, 75, 2, home, dest)
	drive(a, "act-d2", "act-rv-1", t0.Add(40*time.Minute), 20*time.Minute, 5, 75, 70, 2, dest, home)
	drive(a, "act-d3", "act-rv-1", t0.Add(24*time.Hour), 30*time.Minute, 12, 70, 60, 4, home, [2]float64{37.3382, -121.8863})
	drive(a, "act-reparking", "act-rv-1", t0.Add(30*time.Hour), 10*time.Minute, 0.01, 60, 60, 0, home, home)
	drive(a, "act-short", "act-rv-1", t0.Add(31*time.Hour), 30*time.Second, 1, 60, 59, 0.3, home, home)
	drive(b, "act-bd", "act-rv-b", t0, 20*time.Minute, 7, 80, 75, 2, home, dest)

	get := func(uid uuid.UUID, q string) (int, activityResponse, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/activity"+q, nil).WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		handleActivity(df.For(uid), cf.For(uid), sf.For(uid), nil)(w, req)
		var out activityResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	window := "?since=" + base.Add(-time.Hour).Format(time.RFC3339) + "&until=" + base.Add(72*time.Hour).Format(time.RFC3339)

	code, out, raw := get(a, window)
	if code != 200 || len(out.Drives) != 2 || len(out.Charges) != 5 {
		t.Fatalf("all: %d drives=%d charges=%d %s", code, len(out.Drives), len(out.Charges), raw)
	}
	// Newest first: the one-way drive, then the merged round trip (headed by the first leg).
	d3, rt := out.Drives[0], out.Drives[1]
	if d3.ID != "act-d3" || d3.RoundTripLegs != 0 || d3.DrivingSecs != 0 || d3.DistanceMi != 12 || d3.EnergyUsedKWh != 4 {
		t.Errorf("one-way drive = %+v", d3)
	}
	if rt.ID != "act-d1" || rt.RoundTripLegs != 2 || rt.DrivingSecs != 2400 || rt.DistanceMi != 10 || rt.EnergyUsedKWh != 4 ||
		rt.StartSoCPct != 80 || rt.EndSoCPct != 70 || !rt.EndedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("round trip = %+v", rt)
	}
	if rt.EstimatedCost == nil || math.Abs(*rt.EstimatedCost-1.6) > 1e-9 || rt.EstimatedPricePerKWh == nil || math.Abs(*rt.EstimatedPricePerKWh-0.4) > 1e-9 || rt.EstimatedCurrency != "USD" {
		t.Errorf("round trip cost = %v %v %q", rt.EstimatedCost, rt.EstimatedPricePerKWh, rt.EstimatedCurrency)
	}
	// Charges, newest first, with the cluster fallback resolved.
	byID := map[string]activityCharge{}
	for _, c := range out.Charges {
		byID[c.ID] = c
	}
	if out.Charges[0].ID != "act-f1" {
		t.Errorf("not newest first: %s", out.Charges[0].ID)
	}
	for _, id := range []string{"act-h1", "act-h2", "act-h3"} {
		if c := byID[id]; c.Place != "home" || (c.Kind != "ac" && c.Kind != "") {
			t.Errorf("%s: kind %q place %q, want home decided by its cluster", id, c.Kind, c.Place)
		}
	}
	if c := byID["act-p1"]; c.Place != "away" || (c.Kind != "ac" && c.Kind != "") {
		t.Errorf("lone slow charge: kind %q place %q, want away", c.Kind, c.Place)
	}
	if c := byID["act-f1"]; c.Kind != "dcfc" || c.MaxPowerKW != 150 || c.EnergyAddedKWh != 30 || c.Cost != 12 || c.Currency != "USD" {
		t.Errorf("fast charge = %+v", c)
	}
	// Nothing that locates the user, and no other user's data.
	for _, banned := range []string{"lat", "lon", "polyline", "route", "centroid", "member", "radius", "home_location", "37.77", "30.2", "-97.7", "_p~if", "act-b", "act-rv-b"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Errorf("the payload carries %q: %s", banned, raw)
		}
	}
	// Round trips off: three drives, nothing merged.
	if _, o, _ := get(a, window+"&round_trips=0"); len(o.Drives) != 3 || o.Drives[2].RoundTripLegs != 0 {
		t.Errorf("round_trips=0: %d drives", len(o.Drives))
	}
	// A radius too small to close the loop keeps the legs apart? (it is the same spot, so 1 m still merges.)
	if _, o, _ := get(a, window+"&round_trip_radius_m=1"); len(o.Drives) != 2 {
		t.Errorf("1 m radius: %d drives", len(o.Drives))
	}
	// A parked gap longer than the allowed one splits them (the legs are 20 minutes apart).
	if _, o, _ := get(a, window+"&round_trip_gap_min=10"); len(o.Drives) != 3 {
		t.Errorf("10 min gap: %d drives", len(o.Drives))
	}
	// Window (the end is exclusive) and truck.
	if _, o, _ := get(a, "?since="+base.Add(-time.Hour).Format(time.RFC3339)+"&until="+t0.Format(time.RFC3339)); len(o.Drives) != 0 || len(o.Charges) != 5 {
		t.Errorf("window ending where the first drive starts: %d drives %d charges", len(o.Drives), len(o.Charges))
	}
	if _, o, _ := get(a, window+"&vehicle_id=act-rv-b"); len(o.Drives) != 0 || len(o.Charges) != 0 {
		t.Errorf("another user's truck: %+v", o)
	}
	// The other user sees only their own.
	if _, o, _ := get(b, window); len(o.Drives) != 1 || len(o.Charges) != 1 {
		t.Errorf("user b: %d drives %d charges", len(o.Drives), len(o.Charges))
	}
}
