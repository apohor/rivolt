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

// TestListBetweenDownsampled: a 14 h charge at 5 s cadence (10,080 rows)
// over a 1,000-row cap returns the WHOLE span, within the cap, with the
// power peak and the SoC extremes preserved; a window under the cap is
// returned in full and unflagged.
func TestListBetweenDownsampled(t *testing.T) {
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
	uid, err := db.EnsureUser(ctx, pool, "downsample-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "downsample-b")
	if err != nil {
		t.Fatal(err)
	}
	f := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	store := f.For(uid)

	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	const n = 10080 // 14 h at 5 s
	rows := make([]samples.Sample, 0, n)
	for i := 0; i < n; i++ {
		kw := 7.0
		if i == 7777 { // a single-frame spike no even sampler would hit
			kw = 11.5
		}
		rows = append(rows, samples.Sample{
			VehicleID: "rv-ds", At: start.Add(time.Duration(i) * 5 * time.Second),
			BatteryLevelPct: 20 + 60*float64(i)/n, ChargerPowerKW: kw, ChargingState: "charging_active", Source: "live",
		})
	}
	if err := store.InsertBulk(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// Another user's rows in the same window must never leak in.
	if err := f.For(other).InsertBulk(ctx, []samples.Sample{{VehicleID: "rv-o", At: start.Add(time.Hour), ChargerPowerKW: 99, Source: "live"}}); err != nil {
		t.Fatal(err)
	}

	until := start.Add(15 * time.Hour)
	res, err := store.ListBetweenDownsampled(ctx, start.Add(-time.Second), until, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reduced || res.Total != n {
		t.Fatalf("reduced=%v total=%d, want true/%d", res.Reduced, res.Total, n)
	}
	got := res.Samples
	if len(got) == 0 || len(got) > 1000 {
		t.Fatalf("len=%d, want 1..1000", len(got))
	}
	if !got[0].At.Equal(start) || !got[len(got)-1].At.Equal(start.Add((n-1)*5*time.Second)) {
		t.Fatalf("span %v .. %v: truncated", got[0].At, got[len(got)-1].At)
	}
	var peak, minSoC, maxSoC = 0.0, 1000.0, 0.0
	for i, s := range got {
		if i > 0 && !s.At.After(got[i-1].At) {
			t.Fatalf("not strictly increasing at %d", i)
		}
		if s.ChargerPowerKW > peak {
			peak = s.ChargerPowerKW
		}
		if s.BatteryLevelPct < minSoC {
			minSoC = s.BatteryLevelPct
		}
		if s.BatteryLevelPct > maxSoC {
			maxSoC = s.BatteryLevelPct
		}
	}
	if peak != 11.5 {
		t.Errorf("peak = %v, want the 11.5 kW spike kept (and no other user's 99)", peak)
	}
	if minSoC != 20 || maxSoC < 79.9 {
		t.Errorf("soc extremes %v..%v lost", minSoC, maxSoC)
	}

	// Under the cap: everything, unflagged.
	small, err := store.ListBetweenDownsampled(ctx, start.Add(-time.Second), start.Add(10*time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if small.Reduced || len(small.Samples) != 120 {
		t.Fatalf("small window reduced=%v len=%d, want false/120", small.Reduced, len(small.Samples))
	}
}
