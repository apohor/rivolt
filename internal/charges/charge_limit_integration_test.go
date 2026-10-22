//go:build integration

package charges_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

// The charge limit lives on the charge row: a save that carries none keeps the stored
// one, a newer one replaces it, it comes back from the lists and the chart query (null
// when unknown), and the migration's backfill takes the last positive limit within the
// charge's window (and five minutes after) from vehicle_state, ignoring nonsense.
func TestChargeLimitStoredAndBackfilled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "chargelimit-a")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	mk := func(id string, at time.Time, limit *float64) charges.Charge {
		c := sampleCharge(id, "cl-rv", at, "charging_complete")
		c.ChargeLimitPct = limit
		return c
	}
	if err := store.Upsert(ctx, mk("cl-1", base, f64p(80))); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, mk("cl-1", base, nil)); err != nil { // a save that carries none
		t.Fatal(err)
	}
	get := func(id string) *float64 {
		t.Helper()
		c, err := store.GetByExternalID(ctx, id)
		if err != nil || c == nil {
			t.Fatalf("get %s: %v %v", id, c, err)
		}
		return c.ChargeLimitPct
	}
	if v := get("cl-1"); v == nil || *v != 80 {
		t.Fatalf("after a save without a limit: %v, want 80 kept", v)
	}
	if err := store.Upsert(ctx, mk("cl-1", base, f64p(90))); err != nil {
		t.Fatal(err)
	}
	if v := get("cl-1"); v == nil || *v != 90 {
		t.Fatalf("after a newer limit: %v, want 90", v)
	}
	// Unknown stays null; the lists and the chart query carry it.
	if err := store.Upsert(ctx, mk("cl-2", base.Add(3*time.Hour), nil)); err != nil {
		t.Fatal(err)
	}
	if v := get("cl-2"); v != nil {
		t.Errorf("unknown limit read back as %v, want nil", *v)
	}
	pts, err := store.ListChartPoints(ctx, base.Add(-time.Hour), base.Add(24*time.Hour), "")
	if err != nil || len(pts) != 2 {
		t.Fatalf("chart points = %+v, %v", pts, err)
	}
	if pts[0].ChargeLimitPct == nil || *pts[0].ChargeLimitPct != 90 || pts[1].ChargeLimitPct != nil {
		t.Errorf("chart limits = %v, %v; want 90 then null", pts[0].ChargeLimitPct, pts[1].ChargeLimitPct)
	}
	if rows, err := store.ListRecent(ctx, 10); err != nil || len(rows) != 2 || rows[1].ChargeLimitPct == nil || *rows[1].ChargeLimitPct != 90 {
		t.Errorf("ListRecent = %+v, %v", rows, err)
	}

	// Backfill: three more charges with no limit, and vehicle_state around them.
	var vid, uidCheck string
	if err := pool.QueryRowContext(ctx, `SELECT vehicle_id::text, user_id::text FROM charges WHERE user_id = $1 AND external_id = 'cl-1'`, uid).Scan(&vid, &uidCheck); err != nil {
		t.Fatal(err)
	}
	for id, off := range map[string]time.Duration{"cl-bf-a": 6 * time.Hour, "cl-bf-b": 9 * time.Hour, "cl-bf-c": 12 * time.Hour} {
		if err := store.Upsert(ctx, mk(id, base.Add(off), nil)); err != nil {
			t.Fatal(err)
		}
	}
	state := func(at time.Time, limit float64) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (vehicle_id, at, user_id, source, charge_limit_pct) VALUES ($1, $2, $3, 'live', $4)`, vid, at, uid, limit); err != nil {
			t.Fatal(err)
		}
	}
	a, b := base.Add(6*time.Hour), base.Add(9*time.Hour) // sampleCharge lasts 30 minutes
	state(a.Add(5*time.Minute), 70)
	state(a.Add(20*time.Minute), 85)                  // the last positive inside the charge
	state(a.Add(25*time.Minute), 0)                   // 0 = not reported
	state(b.Add(10*time.Minute), 60)                  // inside
	state(b.Add(32*time.Minute), 75)                  // within five minutes after the end: counts
	state(b.Add(40*time.Minute), 100)                 // later than that: not this charge
	state(base.Add(12*time.Hour+10*time.Minute), 250) // nonsense: ignored, so cl-bf-c stays null
	mig, err := os.ReadFile("../db/migrations/0080_charge_limit_pct.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, upd, ok := strings.Cut(string(mig), "UPDATE charges c")
	if !ok {
		t.Fatal("no backfill UPDATE in the migration")
	}
	if _, err := pool.ExecContext(ctx, "UPDATE charges c"+upd); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for id, want := range map[string]float64{"cl-bf-a": 85, "cl-bf-b": 75, "cl-bf-c": 0} {
		v := get(id)
		switch {
		case want == 0 && v != nil:
			t.Errorf("%s backfilled to %v from nonsense, want null", id, *v)
		case want != 0 && (v == nil || *v != want):
			t.Errorf("%s backfilled to %v, want %v", id, v, want)
		}
	}
	// The backfill never overwrites a stored limit.
	if v := get("cl-1"); v == nil || *v != 90 {
		t.Errorf("cl-1 after the backfill: %v, want its own 90", v)
	}
}
