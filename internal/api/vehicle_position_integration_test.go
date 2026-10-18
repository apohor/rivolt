//go:build integration

package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// currentPosition from recorded state: the newest reading with a position, only
// when it is recent, scoped to the user.
func TestCurrentPosition_PG(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`SELECT rivolt_ensure_vehicle_state_partition(now())`)
	alice, _ := db.EnsureUser(ctx, pool, "pos-alice")
	bob, _ := db.EnsureUser(ctx, pool, "pos-bob")
	veh := uuid.New()
	exec(`INSERT INTO vehicles (id, user_id, rivian_vehicle_id) VALUES ($1, $2, 'rv-pos')`, veh, alice)
	now := time.Now().UTC()
	row := func(ago time.Duration, lat, lon, soc any) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, source, lat, lon, battery_level_pct) VALUES ($1, $2, $3, 't', $4, $5, $6)`,
			veh, now.Add(-ago), alice, lat, lon, soc)
	}
	// Newest row has no position: the newest WITH one is used.
	row(2*time.Hour, 35.37, -119.02, 61.5)
	row(10*time.Minute, nil, nil, 60.0)
	row(3*time.Hour, 36.0, -120.0, 70.0)
	got, ok := currentPosition(ctx, nil, pool, alice, "rv-pos", now)
	if !ok || got.Lat != 35.37 || got.Lon != -119.02 || got.SoC != 61.5 || got.VehicleID != "rv-pos" {
		t.Fatalf("position = %+v ok=%v, want the 2 h old row with a position", got, ok)
	}
	// Another user's id for the same vehicle id finds nothing.
	if _, ok := currentPosition(ctx, nil, pool, bob, "rv-pos", now); ok {
		t.Error("bob must not read alice's truck")
	}
	// Too old: 13 h later the same reading is stale.
	if _, ok := currentPosition(ctx, nil, pool, alice, "rv-pos", now.Add(13*time.Hour)); ok {
		t.Error("a reading older than 12 h must not be used")
	}
	// A zero charge (a broken reading) is not a position to plan from.
	exec(`UPDATE vehicle_state SET battery_level_pct = 0 WHERE vehicle_id = $1 AND lat = 35.37`, veh)
	if _, ok := currentPosition(ctx, nil, pool, alice, "rv-pos", now); ok {
		t.Error("a 0 % reading must not be used")
	}
}
