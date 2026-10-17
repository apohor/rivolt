//go:build integration

package samples_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestDriveConditions: each drive gets the median non-zero outside
// temperature and the most-sampled drive mode of its own window; short
// drives, drives outside the period, other vehicles (with the filter) and
// other users' drives are left out.
func TestDriveConditions(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER": "rivolt", "POSTGRES_PASSWORD": "rivolt", "POSTGRES_DB": "rivolt_test",
			},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	pool, err := db.Open(ctx, fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port()))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "drive-conditions-test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "drive-conditions-other")
	if err != nil {
		t.Fatal(err)
	}
	f := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	store := f.For(uid)

	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	// Drive A, 10:00-10:30: outside 0 (the no-reading sentinel), 5, 7, 9 °C;
	// modes everyday ×3, sport ×1. Samples outside the window don't count.
	var rows []samples.Sample
	for _, s := range []struct {
		m    int
		temp float64
		mode string
	}{{0, 0, "everyday"}, {10, 5, "everyday"}, {20, 7, "sport"}, {30, 9, "everyday"}} {
		rows = append(rows, samples.Sample{VehicleID: "rv-e", At: at(10, s.m), OutsideTempC: s.temp, DriveMode: s.mode,
			BatteryLevelPct: 70, OdometerMi: 1000, ShiftState: "D", Source: "live"})
	}
	rows = append(rows, samples.Sample{VehicleID: "rv-e", At: at(11, 0), OutsideTempC: 40, DriveMode: "distance",
		BatteryLevelPct: 70, OdometerMi: 1000, ShiftState: "P", Source: "live"})
	rows = append(rows, samples.Sample{VehicleID: "rv-f", At: at(9, 0), BatteryLevelPct: 70, OdometerMi: 1, Source: "live"})
	if err := store.InsertBulk(ctx, rows); err != nil {
		t.Fatalf("InsertBulk: %v", err)
	}
	if err := f.For(other).InsertBulk(ctx, []samples.Sample{{VehicleID: "rv-x", At: at(9, 0), BatteryLevelPct: 70, OdometerMi: 1, Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	drive := func(user any, rv, ext string, start, end time.Time, mi, kwh, mph float64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, energy_used_kwh, avg_speed_mph, source)
			SELECT $1, v.id, $3, $4, $5, $6, $7, $8, 'live' FROM vehicles v WHERE v.user_id = $1 AND v.rivian_vehicle_id = $2`,
			user, rv, ext, start, end, mi, kwh, mph); err != nil {
			t.Fatal(err)
		}
	}
	drive(uid, "rv-e", "A", at(10, 0), at(10, 30), 20, 8, 40)
	drive(uid, "rv-e", "B", at(12, 0), at(12, 20), 10, 4, 0) // no samples
	drive(uid, "rv-e", "short", at(13, 0), at(13, 0).Add(30*time.Second), 0.1, 0.1, 10)
	drive(uid, "rv-e", "next-day", at(25, 0), at(25, 30), 5, 2, 30) // outside the period
	drive(uid, "rv-f", "F", at(9, 0), at(9, 20), 7, 3, 25)
	drive(other, "rv-x", "X", at(9, 0), at(9, 20), 9, 3, 25)

	got, err := store.DriveConditions(ctx, "", day, day.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("DriveConditions: %v", err)
	}
	ids := map[string]samples.DriveCondition{}
	for _, d := range got {
		ids[d.ID] = d
	}
	if len(got) != 3 || ids["A"].ID == "" || ids["B"].ID == "" || ids["F"].ID == "" {
		t.Fatalf("drives = %+v, want A, B, F", got)
	}
	a := ids["A"]
	if a.OutsideTempC == nil || *a.OutsideTempC != 7 || a.DriveMode != "everyday" || a.DistanceMi != 20 || a.EnergyKWh != 8 || a.AvgSpeedMph != 40 {
		t.Errorf("A = %+v (temp %v), want median 7 °C, everyday, 20 mi, 8 kWh, 40 mph", a, a.OutsideTempC)
	}
	if b := ids["B"]; b.OutsideTempC != nil || b.DriveMode != "" {
		t.Errorf("B without samples = %+v", b)
	}
	if only, err := store.DriveConditions(ctx, "rv-f", day, day.Add(24*time.Hour)); err != nil || len(only) != 1 || only[0].ID != "F" {
		t.Errorf("vehicle filter: %+v, %v", only, err)
	}
}
