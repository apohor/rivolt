//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/vehiclelease"
)

// Lease terms in Postgres: saved per vehicle, the odometer window feeds the
// status, another user cannot overwrite them, and deleting the account
// removes them.
func TestVehicleLeaseStoreAndCascade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "leaseowner")
	other, _ := db.EnsureUser(ctx, pool, "leaseother")
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-lease','R1T') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, r := range []struct {
		ago time.Duration
		mi  float64
	}{{80 * 24 * time.Hour, 5000}, {time.Hour, 7400}} {
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, now.Add(-r.ago)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (vehicle_id, user_id, at, source, odometer_mi) VALUES ($1,$2,$3,'live',$4)`, vid, uid, now.Add(-r.ago), r.mi); err != nil {
			t.Fatal(err)
		}
	}
	st := vehiclelease.NewStore(pool)
	if got, err := st.Get(ctx, uid, vid); err != nil || got != nil {
		t.Fatalf("empty get = %v, %v", got, err)
	}
	terms := vehiclelease.Terms{StartDate: now.AddDate(-1, 0, 0).Format("2006-01-02"), EndDate: now.AddDate(2, 0, 0).Format("2006-01-02"),
		StartOdometerMi: 100, AllowedMilesTotal: 36000, OveragePerMile: 0.25, Currency: "USD"}
	if err := terms.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, uid, vid, terms); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, other, vid, vehiclelease.Terms{StartDate: terms.StartDate, EndDate: terms.EndDate, AllowedMilesTotal: 1, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, uid, vid)
	if err != nil || got == nil || got.AllowedMilesTotal != 36000 {
		t.Fatalf("get = %+v, %v (another user's put must not change it)", got, err)
	}
	latest, past, err := st.Odometer(ctx, uid, vid, now)
	if err != nil || latest == nil || latest.Mi != 7400 || past == nil || past.Mi != 5000 {
		t.Fatalf("odometer = %+v %+v, %v", latest, past, err)
	}
	if err := db.DeleteUser(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_leases WHERE vehicle_id = $1`, vid).Scan(&n); err != nil || n != 0 {
		t.Errorf("lease left after delete: %d, %v", n, err)
	}
}
