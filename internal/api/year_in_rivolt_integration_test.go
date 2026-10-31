//go:build integration

package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
)

// /api/year-in-rivolt over last year: the year's totals come from a date range (not the newest N rows), the coldest drive comes from
// drives that exist and count (an orphan weather row and a re-parking drive's weather are ignored), the year's edges are UTC, the
// gates refuse with no figures, and a window too big for one answer fails closed.
func TestYearInRivolt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	old, _ := db.EnsureUser(ctx, pool, "yr-old")
	young, _ := db.EnsureUser(ctx, pool, "yr-young")
	if _, err := pool.ExecContext(ctx, `UPDATE users SET created_at = now() - interval '90 days' WHERE id = $1`, old); err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)
	y := time.Now().UTC().Year() - 1
	at := func(m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	drive := func(id string, start time.Time, miles, s, e, kwh float64) {
		t.Helper()
		if err := df.For(old).Upsert(ctx, drives.Drive{ID: id, VehicleID: "yr-rv", StartedAt: start, EndedAt: start.Add(30 * time.Minute),
			StartSoCPct: s, EndSoCPct: e, DistanceMi: miles, EnergyUsedKWh: kwh, MaxSpeedMph: 60, AvgSpeedMph: 30, Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 9; i++ {
		drive("yr-d"+strconv.Itoa(i), at(time.Month(1+i%6), 5+i, 9), 10, 80, 70, 4)
	}
	drive("yr-long", at(time.July, 4, 9), 100, 90, 60, 30)
	drive("yr-reparking", at(time.February, 2, 9), 0.01, 60, 60, 0) // the card's set leaves it out
	drive("yr-before", time.Date(y-1, 12, 31, 23, 0, 0, 0, time.UTC), 77, 80, 70, 20)
	drive("yr-after", time.Date(y+1, 1, 1, 0, 30, 0, 0, time.UTC), 66, 80, 70, 20)
	for i := 0; i < 3; i++ {
		if err := cf.For(old).Upsert(ctx, charges.Charge{ID: "yr-c" + strconv.Itoa(i), VehicleID: "yr-rv", StartedAt: at(time.March, 1+i, 20), EndedAt: at(time.March, 1+i, 21),
			StartSoCPct: 30, EndSoCPct: 60, EnergyAddedKWh: 20, MaxPowerKW: 11, Cost: 5, Currency: "USD", PricePerKWh: 0.25, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	// Two saved trips made last year (one with two charging stops, one with none) and one made this year; one truck, an R1S.
	plan := json.RawMessage(`{"Routes":[{"Waypoints":[{"WaypointType":"ORIGIN"},{"WaypointType":"DC_CHARGE_STATION"},{"WaypointType":"DC_CHARGE_STATION"},{"WaypointType":"DESTINATION"}]}]}`)
	tr := trips.NewFactory(pool).For(old)
	for i, created := range []time.Time{at(time.May, 3, 10), at(time.June, 3, 10), time.Now().UTC()} {
		p := plan
		if i == 1 {
			p = json.RawMessage(`{"Routes":[{"Waypoints":[{"WaypointType":"ORIGIN"},{"WaypointType":"DESTINATION"}]}]}`)
		}
		saved, err := tr.Create(ctx, "t"+strconv.Itoa(i), json.RawMessage(`{}`), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE saved_trips SET created_at = $2 WHERE id = $1`, saved.ID, created); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE vehicles SET model = 'R1S' WHERE user_id = $1`, old); err != nil {
		t.Fatal(err)
	}
	weather := func(driveID string, c float64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c)
			VALUES ($1, $2, 30, -97, $3, 'test', $4)`, old, driveID, at(time.January, 1, 0), c); err != nil {
			t.Fatal(err)
		}
	}
	weather("yr-d1", 4)          // a real drive in the year
	weather("yr-d2", -8)         // the coldest real drive
	weather("yr-gone", -40)      // its drive was deleted: the row remains
	weather("yr-reparking", -30) // not a drive the card counts
	weather("yr-before", -25)    // a drive of the year before

	get := func(q string, u uuid.UUID) (int, yearResponse) {
		req := httptest.NewRequest(http.MethodGet, "/api/year-in-rivolt"+q, nil).WithContext(auth.WithUser(ctx, u))
		w := httptest.NewRecorder()
		handleYearInRivolt(pool, u, df.For(u), cf.For(u), sf.For(u), trips.NewFactory(pool).For(u), nil)(w, req)
		var out yearResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := get("?year="+strconv.Itoa(y), old)
	if code != 200 || !out.Eligible || out.Year != y {
		t.Fatalf("old account: %d %+v", code, out)
	}
	if out.Miles != 190 || out.KWh != 66 || out.ChargeCount != 3 {
		t.Errorf("totals miles=%v kwh=%v charges=%d: the year's edges and the re-parking drive are out", out.Miles, out.KWh, out.ChargeCount)
	}
	if out.LongestTrip == nil || out.LongestTrip.Miles != 100 || out.LongestTrip.Month != strconv.Itoa(y)+"-07" {
		t.Errorf("longest trip %+v", out.LongestTrip)
	}
	// d2 is the March drive (i=2): the month is the drive's, not the weather row's sampled_at (January).
	if out.ColdestDrive == nil || math.Abs(out.ColdestDrive.TempF-17.6) > 1e-6 || out.ColdestDrive.Month != strconv.Itoa(y)+"-03" {
		t.Errorf("coldest drive %+v: want -8 C (17.6 F) in the drive's month, not the orphan, the re-parking or the other year's", out.ColdestDrive)
	}
	if out.DriveCount != 10 || out.PlannedTrips == nil || *out.PlannedTrips != 2 || *out.PlannedStops != 2 || out.Model != "R1S" || out.SinceMonth != "" {
		t.Errorf("drive count %d, planned %v/%v, model %q, since %q", out.DriveCount, out.PlannedTrips, out.PlannedStops, out.Model, out.SinceMonth)
	}
	if out.MostEfficientLong == nil || out.MostEfficientLong.Miles != 100 || out.MostEfficientLong.Month != strconv.Itoa(y)+"-07" {
		t.Errorf("most efficient long drive %+v", out.MostEfficientLong)
	}
	if sh := out.EnergyShares; sh != nil && math.Abs(sh.Home+sh.Away+sh.Fast-1) > 1e-9 {
		t.Errorf("shares do not sum to 1: %+v", sh)
	}
	if out.Cost == nil || out.Cost.Amount != 15 || out.Cost.Currency != "USD" || out.Cost.Estimated {
		t.Errorf("cost %+v", out.Cost)
	}

	if code, out := get("?year="+strconv.Itoa(y), young); code != 200 || out.Eligible || out.Reason != "account_too_new" || out.Miles != 0 {
		t.Errorf("young account: %d %+v", code, out)
	}
	if code, _ := get("?year=2019", old); code != http.StatusBadRequest {
		t.Errorf("year 2019: %d", code)
	}
}
