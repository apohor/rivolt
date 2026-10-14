//go:build integration

package samples_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// TestSleepActivityNullStates: an interval with no recorded gear or
// charging state (NULL, as on older and imported rows) is parked and
// not charging, so awake time there counts as idle-awake.
func TestSleepActivityNullStates(t *testing.T) {
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
	uid, err := db.EnsureUser(ctx, pool, "sleep-activity-test")
	if err != nil {
		t.Fatal(err)
	}
	store := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	// One UTC day, a row every 10 min:
	//   00:00-02:00 asleep, parked
	//   02:00-04:00 awake, gear and charging state NULL (set below)
	//   04:00-06:00 awake, parked
	//   06:00-07:00 driving (excluded)
	// → 2 h asleep, 4 h idle-awake (2 h before the COALESCE).
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	var rows []samples.Sample
	for i := 0; i <= 42; i++ {
		at := day.Add(time.Duration(i) * 10 * time.Minute)
		s := samples.Sample{VehicleID: "rv-s", At: at, BatteryLevelPct: 70, OdometerMi: 1000,
			ShiftState: "P", ChargingState: "charger_disconnected", PowerState: "standby", Source: "live"}
		switch h := at.Sub(day); {
		case h < 2*time.Hour:
			s.PowerState = "sleep"
		case h >= 6*time.Hour:
			s.ShiftState, s.SpeedMph, s.PowerState = "D", 40, "go"
		}
		rows = append(rows, s)
	}
	if err := store.InsertBulk(ctx, rows); err != nil {
		t.Fatalf("InsertBulk: %v", err)
	}
	if _, err := pool.ExecContext(ctx, `
		UPDATE vehicle_state SET shift_state = NULL, charging_state = NULL
		 WHERE at >= $1::timestamptz AND at < $2::timestamptz`, day.Add(2*time.Hour), day.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := store.SleepActivity(ctx, "rv-s", day, day.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("SleepActivity: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("days = %+v, want one", got)
	}
	if math.Abs(got[0].AsleepH-2) > 1e-9 || math.Abs(got[0].IdleAwakeH-4) > 1e-9 {
		t.Errorf("asleep %.4f h, idle-awake %.4f h; want 2, 4", got[0].AsleepH, got[0].IdleAwakeH)
	}
}
