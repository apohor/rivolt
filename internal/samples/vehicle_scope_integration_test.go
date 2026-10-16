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

// TestListBetweenVehicleScope: with two trucks recording over the same
// window, a vehicle filter returns only that truck's rows (plain and
// downsampled), no filter returns both, and a vehicle id that is
// unknown or another user's matches nothing.
func TestListBetweenVehicleScope(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_USER": "rivolt", "POSTGRES_PASSWORD": "rivolt", "POSTGRES_DB": "rivolt_test"},
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
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "vscope-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "vscope-b")
	if err != nil {
		t.Fatal(err)
	}
	f := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	store := f.For(uid)

	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	const n = 300
	var rows []samples.Sample
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Second)
		rows = append(rows,
			samples.Sample{VehicleID: "rv-1", At: at, BatteryLevelPct: 80, Source: "live"},
			samples.Sample{VehicleID: "rv-2", At: at, BatteryLevelPct: 40, Source: "live"})
	}
	if err := store.InsertBulk(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := f.For(other).InsertBulk(ctx, []samples.Sample{{VehicleID: "rv-other", At: start.Add(time.Minute), Source: "live"}}); err != nil {
		t.Fatal(err)
	}
	since, until := start.Add(-time.Second), start.Add(time.Hour)

	only := func(rs []samples.Sample, id string) bool {
		for _, s := range rs {
			if s.VehicleID != id {
				return false
			}
		}
		return true
	}
	for _, u := range []time.Time{until, {}} { // bounded and open-ended
		got, err := store.ListBetweenVehicle(ctx, "rv-1", since, u, 10000)
		if err != nil || len(got) != n || !only(got, "rv-1") {
			t.Fatalf("rv-1 (until %v) = %d rows, %v", u, len(got), err)
		}
	}
	if all, err := store.ListBetweenVehicle(ctx, "", since, until, 10000); err != nil || len(all) != 2*n {
		t.Fatalf("no filter = %d, %v; want %d", len(all), err, 2*n)
	}
	for _, id := range []string{"rv-nope", "rv-other"} {
		if got, err := store.ListBetweenVehicle(ctx, id, since, until, 10000); err != nil || len(got) != 0 {
			t.Fatalf("%s = %d rows, %v; want none", id, len(got), err)
		}
	}
	// Downsampled: the total and the buckets count only the chosen truck.
	res, err := store.ListBetweenDownsampledVehicle(ctx, "rv-2", since, until, 100)
	if err != nil || !res.Reduced || res.Total != n || len(res.Samples) == 0 || len(res.Samples) > 100 || !only(res.Samples, "rv-2") {
		t.Fatalf("downsampled rv-2: reduced %v total %d rows %d, %v", res.Reduced, res.Total, len(res.Samples), err)
	}
	small, err := store.ListBetweenDownsampledVehicle(ctx, "rv-1", since, until, 1000)
	if err != nil || small.Reduced || len(small.Samples) != n || !only(small.Samples, "rv-1") {
		t.Fatalf("under cap rv-1: reduced %v rows %d, %v", small.Reduced, len(small.Samples), err)
	}
}
