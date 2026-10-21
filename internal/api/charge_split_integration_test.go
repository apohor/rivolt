//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
)

// /api/charge-split labels charges as the web's locationSplit does: three slow charges
// at one spot are Home by their cluster alone (no home is set, so the classification
// cannot say), a lone slow charge elsewhere is Away, a 150 kW charge is Fast, a charge
// with no GPS fix and no classification is in no bucket and not in total. The window,
// one truck, a short charge, another user's rows and the payload's contents are checked.
func TestChargeSplit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "cs-a")
	b, _ := db.EnsureUser(ctx, pool, "cs-b")
	resolvers := db.NewVehicleResolverFactory(pool)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	charge := func(uid uuid.UUID, id, vid string, at time.Time, dur time.Duration, kwh, peak, lat, lon float64) {
		t.Helper()
		if err := cf.For(uid).Upsert(ctx, charges.Charge{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(dur),
			StartSoCPct: 30, EndSoCPct: 60, EnergyAddedKWh: kwh, MaxPowerKW: peak, Lat: lat, Lon: lon,
			FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	h := 40 * time.Minute
	charge(a, "cs-h1", "cs-rv-1", base, h, 10, 11, 30.2, -97.7)
	charge(a, "cs-h2", "cs-rv-1", base.Add(2*time.Hour), h, 10, 11, 30.2, -97.7)
	charge(a, "cs-h3", "cs-rv-2", base.Add(4*time.Hour), h, 10, 11, 30.2001, -97.7001) // the other truck
	charge(a, "cs-p1", "cs-rv-1", base.Add(6*time.Hour), h, 5, 7, 40.0, -100.0)
	charge(a, "cs-f1", "cs-rv-1", base.Add(8*time.Hour), h, 30, 150, 35.0, -90.0)
	charge(a, "cs-nogps", "cs-rv-1", base.Add(10*time.Hour), h, 7, 11, 0, 0)
	charge(a, "cs-short", "cs-rv-1", base.Add(12*time.Hour), 20*time.Second, 1, 11, 30.2, -97.7)
	charge(a, "cs-old", "cs-rv-1", base.Add(-300*time.Hour), h, 10, 11, 30.2, -97.7) // before the window
	charge(b, "cs-b1", "cs-rv-b", base, h, 99, 11, 30.2, -97.7)

	get := func(uid uuid.UUID, q string) (int, chargeSplit, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/charge-split"+q, nil).WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		handleChargeSplit(cf.For(uid), sf.For(uid), nil)(w, req)
		var out chargeSplit
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	window := "?since=" + base.Add(-24*time.Hour).Format(time.RFC3339) + "&until=" + base.Add(24*time.Hour).Format(time.RFC3339)

	code, got, raw := get(a, window)
	want := chargeSplit{Home: splitBucket{3, 30}, Away: splitBucket{1, 5}, Fast: splitBucket{1, 30}, Total: 5}
	if code != 200 || got != want {
		t.Fatalf("all trucks: %d %+v, want %+v (%s)", code, got, want, raw)
	}
	for _, banned := range []string{"lat", "lon", "id", "cost", "place", "network", "30.2", "-97.7", "cs-"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Errorf("the payload carries %q: %s", banned, raw)
		}
	}
	// One truck: its two home charges, the away one and the fast one (cs-h3 is the other truck's).
	if _, got, _ := get(a, window+"&vehicle_id=cs-rv-1"); got != (chargeSplit{Home: splitBucket{2, 20}, Away: splitBucket{1, 5}, Fast: splitBucket{1, 30}, Total: 4}) {
		t.Errorf("truck 1: %+v", got)
	}
	if _, got, _ := get(a, window+"&vehicle_id=cs-rv-b"); got != (chargeSplit{}) {
		t.Errorf("another user's truck: %+v", got)
	}
	// The old charge is outside this window and the end is exclusive.
	if _, got, _ := get(a, "?since="+base.Add(-400*time.Hour).Format(time.RFC3339)+"&until="+base.Add(-299*time.Hour).Format(time.RFC3339)); got.Total != 1 {
		t.Errorf("a window around the old charge: %+v", got)
	}
	if _, got, _ := get(a, "?since="+base.Format(time.RFC3339)+"&until="+base.Format(time.RFC3339)); got != (chargeSplit{}) {
		t.Errorf("an empty window: %+v", got)
	}
	// The other user sees only their own (alone, their one slow charge has no home cluster).
	if _, got, _ := get(b, window); got.Total != 1 || got.Home.EnergyKWh+got.Away.EnergyKWh != 99 {
		t.Errorf("user b: %+v", got)
	}
}
