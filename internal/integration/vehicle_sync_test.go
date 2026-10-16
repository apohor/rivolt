//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// The Rivian vehicle sync must persist trim and the hero image: both
// vehicles.trim and vehicles.image_url were empty for every vehicle because
// the upsert never wrote them. A later sync that doesn't know a value must
// not erase it, and the vehicle-reported pack_kwh must survive.
func TestUpsertVehicleFromSyncPersistsTrimAndImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "vehicle-sync")
	if err != nil {
		t.Fatal(err)
	}
	row := func() (trim, image sql.NullString, pack sql.NullFloat64) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT trim, image_url, pack_kwh FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = 'rv-1'`, uid).Scan(&trim, &image, &pack); err != nil {
			t.Fatal(err)
		}
		return
	}

	// First sync: trim known, image not yet.
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: "rv-1", Model: "R1S", ModelYear: 2025, Trim: "G2-LRG-DM", PackKWh: 141.5}); err != nil {
		t.Fatal(err)
	}
	if trim, image, _ := row(); trim.String != "G2-LRG-DM" || image.Valid {
		t.Fatalf("after first sync trim %q image %+v", trim.String, image)
	}

	// The truck reports its real capacity, then a sync with the image but
	// without a trim and with a different InferPackKWh guess.
	if _, err := pool.ExecContext(ctx, `UPDATE vehicles SET pack_kwh = 135.0 WHERE user_id = $1 AND rivian_vehicle_id = 'rv-1'`, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: "rv-1", Model: "R1S", PackKWh: 141.5, ImageURL: "https://img.example/a.webp"}); err != nil {
		t.Fatal(err)
	}
	trim, image, pack := row()
	if trim.String != "G2-LRG-DM" || image.String != "https://img.example/a.webp" || pack.Float64 != 135.0 {
		t.Fatalf("after second sync trim %q image %q pack %v; want trim kept, image set, reported pack kept", trim.String, image.String, pack.Float64)
	}

	// SetVehicleImageURL replaces the image; an empty URL is a no-op.
	if err := db.SetVehicleImageURL(ctx, pool, uid, "rv-1", "https://img.example/b.webp"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetVehicleImageURL(ctx, pool, uid, "rv-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, image, _ := row(); image.String != "https://img.example/b.webp" {
		t.Fatalf("image after SetVehicleImageURL = %q", image.String)
	}
}
