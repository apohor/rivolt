//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
)

// The monitor's vehicle metadata writes (vehicle-info upsert, battery-capacity write-through) carry
// the group lease when the context has a token: the owner's go through, a stale epoch, another pod
// or a missing lease is refused with nothing changed. Without a token (connect, /api/vehicles)
// the upsert runs as before.
func TestVehicleMetadataWrites_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "vehmeta-fence")
	if err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	owner := fence.With(ctx, fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epoch})

	// Plain path (connect): creates the row.
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: "rv-m", Model: "R1T", ModelYear: 2024}); err != nil {
		t.Fatalf("plain upsert: %v", err)
	}
	if err := db.UpsertVehicleFromSync(owner, pool, uid, db.VehicleSync{RivianID: "rv-m", Trim: "PKG-ADV", PackKWh: 135, ImageURL: "https://img/a"}); err != nil {
		t.Fatalf("owner upsert: %v", err)
	}
	if err := db.SetVehiclePackKWh(owner, pool, uid, "rv-m", 131.5); err != nil {
		t.Fatalf("owner pack: %v", err)
	}
	// Unchanged value: no row matches, not a refusal.
	if err := db.SetVehiclePackKWh(owner, pool, uid, "rv-m", 131.5); err != nil {
		t.Fatalf("owner pack, same value: %v", err)
	}
	type row struct {
		model, trim, image string
		year               int
		pack               float64
	}
	read := func() row {
		var r row
		if err := pool.QueryRowContext(ctx, `SELECT model, trim, image_url, model_year, pack_kwh FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = 'rv-m'`, uid).
			Scan(&r.model, &r.trim, &r.image, &r.year, &r.pack); err != nil {
			t.Fatal(err)
		}
		return r
	}
	want := row{model: "R1T", trim: "PKG-ADV", image: "https://img/a", year: 2024, pack: 131.5}
	if got := read(); got != want {
		t.Fatalf("after the owner's writes: %+v, want %+v", got, want)
	}

	for i, tok := range []fence.Token{
		{Group: uid.String(), Pod: "pod-a", Epoch: epoch - 1},
		{Group: uid.String(), Pod: "pod-b", Epoch: epoch},
		{Group: uuid.NewString(), Pod: "pod-a", Epoch: epoch},
	} {
		bad := fence.With(ctx, tok)
		for name, err := range map[string]error{
			"upsert":     db.UpsertVehicleFromSync(bad, pool, uid, db.VehicleSync{RivianID: "rv-m", Trim: "OTHER", ImageURL: "https://img/b"}),
			"upsert new": db.UpsertVehicleFromSync(bad, pool, uid, db.VehicleSync{RivianID: "rv-new", Model: "R2"}),
			"pack":       db.SetVehiclePackKWh(bad, pool, uid, "rv-m", 99),
		} {
			if !errors.Is(err, fence.ErrFenced) {
				t.Errorf("token %d, %s: %v; want fence.ErrFenced", i, name, err)
			}
		}
	}
	if got := read(); got != want {
		t.Errorf("refused writes changed the row: %+v, want %+v", got, want)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 1 {
		t.Errorf("vehicles for the user = %d, %v; want 1 (no refused insert)", n, err)
	}
}
