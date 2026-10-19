//go:build integration

package drives_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/testpg"
)

// "Delete your history" must also remove what hangs off each drive without a
// foreign key: the recap, efficiency analysis and the weather (a coarse start
// location and the hour). Another user's rows stay.
func TestResetRemovesPerDriveRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, err := db.EnsureUser(ctx, pool, "drivereset-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.EnsureUser(ctx, pool, "drivereset-b")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []struct {
		id uuid.UUID
		d  string
	}{{a, "reset-d1"}, {b, "reset-d2"}} {
		exec(`INSERT INTO drive_recaps (user_id, drive_id, model, recap) VALUES ($1,$2,'m','r')`, u.id, u.d)
		exec(`INSERT INTO drive_efficiency (user_id, drive_id, model, analysis_text, result_json) VALUES ($1,$2,'m','t','{}')`, u.id, u.d)
		exec(`INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c) VALUES ($1,$2,30.3,-97.7,now(),'open-meteo',5)`, u.id, u.d)
		exec(`INSERT INTO drive_weather_series (user_id, drive_id, sampled_at, cadence_minutes, coarse_lat, coarse_lon, provider) VALUES ($1,$2,now(),60,30.3,-97.7,'open-meteo')`, u.id, u.d)
	}
	st := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(a)
	if st == nil {
		t.Fatal("no store")
	}
	if _, err := st.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	count := func(table string, u uuid.UUID) int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, u).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, tbl := range []string{"drive_recaps", "drive_efficiency", "drive_weather", "drive_weather_series"} {
		if n := count(tbl, a); n != 0 {
			t.Errorf("%s: %d rows left for the user who reset", tbl, n)
		}
		if n := count(tbl, b); n != 1 {
			t.Errorf("%s: %d rows for the other user, want 1", tbl, n)
		}
	}
}
