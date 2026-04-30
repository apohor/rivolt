//go:build integration

// Package integration_test exercises the rivolt boot path end-to-end
// against a real Postgres (testcontainers) and the in-process Rivian
// mock client.
//
// The goal of this test is to gate replicaCount > 1: it proves that
// the cold-start wiring — DB.Open → migrations → user/vehicle resolve
// → samples.InsertBatch → leases.Coordinator reconcile — actually
// produces a vehicle_state row and a subscription_leases row, without
// hitting the live Rivian API.
//
// Run with:
//
//	go test -tags=integration ./internal/integration/...
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
)

// startPostgres spins up postgres:16-alpine in a container and
// returns a DSN reachable from the host. The container is torn down
// via t.Cleanup. CI without Docker should set RIVOLT_SKIP_INTEGRATION=1.
func startPostgres(ctx context.Context, t *testing.T) string {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "rivolt",
			"POSTGRES_PASSWORD": "rivolt",
			"POSTGRES_DB":       "rivolt_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Terminate(context.Background())
	})
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port())
}

// TestBootToRecord asserts the cold-boot wiring records a sample and
// claims a lease.
//
// Steps:
//  1. Start Postgres and let db.Open run every embedded migration.
//  2. Seed the canonical "local" user and resolve the mock vehicle.
//  3. Drive the mock Rivian client through Login → Vehicles → State.
//  4. Translate the State into a samples.Sample and InsertBatch.
//  5. Wire a real leases.Coordinator (Postgres-backed) and trigger
//     one reconcile against a DB-only vehicle source.
//  6. Assert: vehicle_state has exactly 1 row for our user, and
//     subscription_leases has the lease under our pod_id.
func TestBootToRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// 1. Postgres + migrations.
	dsn := startPostgres(ctx, t)
	pgPool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pgPool.Close() })

	// 2. User + vehicle resolver.
	userID, err := db.EnsureUser(ctx, pgPool, "local")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if userID == uuid.Nil {
		t.Fatal("EnsureUser returned nil UUID")
	}
	resolver := db.NewVehicleResolver(pgPool, userID)

	// 3. Mock Rivian client end-to-end.
	mc := rivian.NewMock()
	if err := mc.Login(ctx, rivian.Credentials{Email: "test@example.com", Password: "pw"}); err != nil {
		t.Fatalf("mock Login: %v", err)
	}
	vehicles, err := mc.Vehicles(ctx)
	if err != nil {
		t.Fatalf("mock Vehicles: %v", err)
	}
	if len(vehicles) != 1 {
		t.Fatalf("mock returned %d vehicles, want 1", len(vehicles))
	}
	rivianVehicleID := vehicles[0].ID
	state, err := mc.State(ctx, rivianVehicleID)
	if err != nil {
		t.Fatalf("mock State: %v", err)
	}

	// 4. Sample write — the "record" half of boot-to-record.
	samplesStore, err := samples.OpenStore(pgPool, userID, resolver)
	if err != nil {
		t.Fatalf("samples.OpenStore: %v", err)
	}
	// km → mi conversion mirrors what production code does when it
	// translates rivian.State into samples.Sample. We don't need it
	// to be exact; we just need a row with the right shape.
	const kmToMi = 0.621371
	batch := []samples.Sample{{
		VehicleID:       rivianVehicleID,
		At:              state.At,
		BatteryLevelPct: state.BatteryLevelPct,
		RangeMi:         state.DistanceToEmpty * kmToMi,
		OdometerMi:      state.OdometerKm * kmToMi,
		Lat:             state.Latitude,
		Lon:             state.Longitude,
		SpeedMph:        state.SpeedKph * kmToMi,
		ShiftState:      state.Gear,
		ChargingState:   state.ChargerState,
		ChargerPowerKW:  state.ChargerPowerKW,
		ChargeLimitPct:  state.ChargeTargetPct,
		InsideTempC:     state.CabinTempC,
		OutsideTempC:    state.OutsideTempC,
		Source:          "live",
	}}
	if err := samplesStore.InsertBatch(ctx, batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	// 5. Lease coordinator — drive one reconcile and observe acquire.
	leaseStore, err := leases.NewStore(pgPool, "test-pod-1")
	if err != nil {
		t.Fatalf("leases.NewStore: %v", err)
	}
	acquired := make(chan string, 4)
	released := make(chan string, 4)
	vehicleSource := leases.NewVehicleSource(
		nil, // no in-process StateMonitor in this test
		logger,
		// DB source — by now the resolver has upserted the vehicles
		// row, so this must yield exactly the mock vehicle ID.
		func(qctx context.Context) ([]string, error) {
			return leases.QueryStringColumn(qctx, pgPool,
				`SELECT DISTINCT rivian_vehicle_id FROM vehicles WHERE rivian_vehicle_id <> ''`)
		},
	)
	coord := leases.NewCoordinator(
		leaseStore,
		vehicleSource,
		func(vid string) { acquired <- vid },
		func(vid string) { released <- vid },
		logger,
	)
	runCtx, runCancel := context.WithCancel(ctx)
	t.Cleanup(runCancel)
	runDone := make(chan struct{})
	go func() {
		_ = coord.Run(runCtx)
		close(runDone)
	}()
	// Run does an immediate reconcile on entry; TriggerReconcile is
	// belt-and-suspenders for the slow-CI case.
	coord.TriggerReconcile()

	select {
	case vid := <-acquired:
		if vid != rivianVehicleID {
			t.Fatalf("acquired %q, want %q", vid, rivianVehicleID)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for lease acquire")
	}

	// 6. Assertions.
	t.Run("vehicle_state row recorded", func(t *testing.T) {
		var count int
		if err := pgPool.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM vehicle_state WHERE user_id = $1`, userID,
		).Scan(&count); err != nil {
			t.Fatalf("count vehicle_state: %v", err)
		}
		if count != 1 {
			t.Fatalf("vehicle_state rows = %d, want 1", count)
		}
	})

	t.Run("vehicle_state row matches mock state", func(t *testing.T) {
		var pct float64
		var source string
		if err := pgPool.QueryRowContext(ctx,
			`SELECT battery_level_pct, source FROM vehicle_state WHERE user_id = $1`, userID,
		).Scan(&pct, &source); err != nil {
			t.Fatalf("scan vehicle_state: %v", err)
		}
		if pct != state.BatteryLevelPct {
			t.Errorf("battery_level_pct = %v, want %v", pct, state.BatteryLevelPct)
		}
		if source != "live" {
			t.Errorf("source = %q, want %q", source, "live")
		}
	})

	t.Run("subscription_leases row claimed", func(t *testing.T) {
		var podID string
		if err := pgPool.QueryRowContext(ctx,
			`SELECT pod_id FROM subscription_leases WHERE vehicle_id = $1`, rivianVehicleID,
		).Scan(&podID); err != nil {
			t.Fatalf("query subscription_leases: %v", err)
		}
		if podID != "test-pod-1" {
			t.Errorf("pod_id = %q, want %q", podID, "test-pod-1")
		}
	})

	t.Run("idempotent re-record", func(t *testing.T) {
		// Same (vehicle_id, at) tuple → ON CONFLICT DO NOTHING. Re-
		// running the same batch must not double-count.
		if err := samplesStore.InsertBatch(ctx, batch); err != nil {
			t.Fatalf("re-InsertBatch: %v", err)
		}
		var count int
		if err := pgPool.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM vehicle_state WHERE user_id = $1`, userID,
		).Scan(&count); err != nil {
			t.Fatalf("count vehicle_state: %v", err)
		}
		if count != 1 {
			t.Errorf("after re-insert vehicle_state rows = %d, want 1", count)
		}
	})

	// Drain runDone to keep the goroutine from outliving the test.
	runCancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Log("coordinator Run did not exit within 5s of cancel")
	}
}
