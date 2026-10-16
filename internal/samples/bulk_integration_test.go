//go:build integration

// InsertBulk integration test: multi-row chunks across a month
// boundary, idempotency, and column parity with InsertBatch.
//
// Run with: go test -tags=integration ./internal/samples/...
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

func TestInsertBulk(t *testing.T) {
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
	uid, err := db.EnsureUser(ctx, pool, "bulk-test")
	if err != nil {
		t.Fatal(err)
	}
	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	// 1,200 samples spanning a month boundary → 3 chunks, 2 partitions.
	t0 := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	alt, tire, temp := 150.0, 2.6, 31.5
	fix := t0.Add(-time.Second)
	var batch []samples.Sample
	for i := 0; i < 1200; i++ {
		batch = append(batch, samples.Sample{
			VehicleID: "rv-bulk", At: t0.Add(time.Duration(i) * 5 * time.Second),
			BatteryLevelPct: 70, RangeMi: 250, OdometerMi: 1000 + float64(i)*0.01,
			Lat: 30.27, Lon: -97.74, SpeedMph: 40, ShiftState: "D", DriveMode: "everyday",
			ChargingState: "charger_disconnected", Source: "live", DriveNumber: 7,
			LocationFixAt: &fix, AltitudeM: &alt, TirePressureMinBar: &tire,
			PackTempAvgC: &temp, PackTempMaxC: &temp, PackTempMinC: &temp, PowerState: "go",
			ChargerStatus: "chrgr_sts_not_connected",
		})
	}
	if err := store.InsertBulk(ctx, batch); err != nil {
		t.Fatalf("InsertBulk: %v", err)
	}
	if err := store.InsertBulk(ctx, batch[:600]); err != nil { // idempotent re-run
		t.Fatalf("InsertBulk again: %v", err)
	}
	if n, err := store.Count(ctx); err != nil || n != 1200 {
		t.Fatalf("Count = %d, %v; want 1200", n, err)
	}

	// Column parity: the same sample via InsertBatch on another vehicle
	// reads back identically (modulo vehicle).
	twin := batch[0]
	twin.VehicleID = "rv-batch"
	if err := store.InsertBatch(ctx, []samples.Sample{twin}); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	var diff int
	if err := pool.QueryRowContext(ctx, `
		SELECT count(*) FROM (
		  SELECT at, battery_level_pct, range_mi, odometer_mi, lat, lon, location_fix_at, speed_mph,
		         shift_state, drive_mode, charging_state, drive_number, source, altitude_m,
		         tire_pressure_min_bar, pack_temp_avg_c, power_state, charger_status
		  FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id
		  WHERE v.rivian_vehicle_id = 'rv-bulk' AND vs.at = $1
		  EXCEPT
		  SELECT at, battery_level_pct, range_mi, odometer_mi, lat, lon, location_fix_at, speed_mph,
		         shift_state, drive_mode, charging_state, drive_number, source, altitude_m,
		         tire_pressure_min_bar, pack_temp_avg_c, power_state, charger_status
		  FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id
		  WHERE v.rivian_vehicle_id = 'rv-batch'
		) d`, t0).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if diff != 0 {
		t.Errorf("InsertBulk and InsertBatch rows differ")
	}
	var cst string
	if err := pool.QueryRowContext(ctx, `SELECT charger_status FROM vehicle_state vs JOIN vehicles v ON v.id = vs.vehicle_id
		WHERE v.rivian_vehicle_id = 'rv-batch' AND vs.at = $1`, t0).Scan(&cst); err != nil || cst != "chrgr_sts_not_connected" {
		t.Errorf("charger_status = %q (%v), want chrgr_sts_not_connected", cst, err)
	}
}
