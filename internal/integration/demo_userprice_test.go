//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/rivian"
)

// TestDemoSampleUserPrice: the pass prices the closed, unpriced hotel
// session as user $15, leaves an OPEN one (the recorder would overwrite
// it), another user's, and non-hotel sessions alone, and is idempotent.
func TestDemoSampleUserPrice(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserFull(ctx, pool, demo.Username, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	var hotel time.Time
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for m := 0; m < 1100*60 && hotel.IsZero(); m++ {
		if at := base.Add(time.Duration(m) * time.Minute); func() bool { _, _, ok := rivian.SampleUserPrice(at); return ok }() {
			hotel = at
		}
	}
	if hotel.IsZero() {
		t.Fatal("no hotel session in the corpus loop")
	}
	veh := map[uuid.UUID]uuid.UUID{}
	for _, u := range []uuid.UUID{uid, other} {
		var v uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2) RETURNING id`, u, "rv-"+u.String()[:8]).Scan(&v); err != nil {
			t.Fatal(err)
		}
		veh[u] = v
	}
	ins := func(u uuid.UUID, ext string, at time.Time, state string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh, final_state)
			VALUES ($1,$2,$3,$4,$5,'live',10,$6)`, u, veh[u], ext, at, at.Add(time.Hour), state); err != nil {
			t.Fatal(err)
		}
	}
	ins(uid, "hotel", hotel, "charging_complete")
	// The same session one corpus loop later, still open.
	next := hotel.Add(1080 * time.Hour)
	if _, _, ok := rivian.SampleUserPrice(next); !ok {
		t.Fatalf("corpus loop is not 1080h: no hotel session at %v", next)
	}
	ins(uid, "open-hotel", next, "charging_active")
	ins(uid, "plain", hotel.Add(-9*time.Hour), "charging_complete")
	ins(other, "theirs", hotel, "charging_complete")

	cf := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	for i := 0; i < 2; i++ { // second run: idempotent
		if _, err := demo.ApplySampleUserPrices(ctx, pool, cf.For(uid)); err != nil {
			t.Fatal(err)
		}
	}
	get := func(u uuid.UUID, ext string) (cost *float64, src *string) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT cost::float8, cost_source FROM charges WHERE user_id=$1 AND external_id=$2`, u, ext).Scan(&cost, &src); err != nil {
			t.Fatal(err)
		}
		return
	}
	if c, s := get(uid, "hotel"); c == nil || *c != 15 || s == nil || *s != "user" {
		t.Fatalf("hotel: cost=%v source=%v, want 15/user", c, s)
	}
	for _, x := range []struct {
		u   uuid.UUID
		ext string
	}{{uid, "plain"}, {uid, "open-hotel"}, {other, "theirs"}} {
		if c, s := get(x.u, x.ext); c != nil || s != nil {
			t.Errorf("%s was priced: %v %v", x.ext, c, s)
		}
	}
}
