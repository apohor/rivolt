//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// OTA details live in vehicles.metadata beside the profile: stored without
// disturbing other keys, claimed once a window across pods, scoped to the
// user's own vehicle, and gone with the row.
func TestVehicleOTAStoreAndClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "ota-owner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "ota-other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, metadata) VALUES ($1,'rv-1','R1S','{"profile":{"keep":"me"}}')`, uid); err != nil {
		t.Fatal(err)
	}

	if ok, err := db.ClaimOTAFetch(ctx, pool, uid, "rv-1", time.Hour); err != nil || !ok {
		t.Fatalf("first claim = %v, err %v", ok, err)
	}
	if ok, _ := db.ClaimOTAFetch(ctx, pool, uid, "rv-1", time.Hour); ok {
		t.Error("a second claim inside the window won")
	}
	if ok, _ := db.ClaimOTAFetch(ctx, pool, other, "rv-1", time.Hour); ok {
		t.Error("another user claimed a vehicle that is not theirs")
	}
	if ok, err := db.ClaimOTAFetch(ctx, pool, uid, "rv-1", 0); err != nil || !ok {
		t.Errorf("claim with a zero window = %v, err %v", ok, err)
	}
	if ok, _ := db.ClaimOTAFetch(ctx, pool, uid, "rv-missing", 0); ok {
		t.Error("claimed a vehicle that does not exist")
	}

	ota := json.RawMessage(`{"current":{"version":"2026.30.0","release_notes_url":"https://rivian.com/n"},"available":{"version":"2026.38.1"},"fetched_at":"2026-10-17T10:00:00Z"}`)
	if err := db.SetVehicleOTA(ctx, pool, uid, "rv-1", ota); err != nil {
		t.Fatal(err)
	}
	if err := db.SetVehicleOTA(ctx, pool, other, "rv-1", ota); err == nil {
		t.Error("another user wrote OTA details to a vehicle that is not theirs")
	}
	got, err := db.ListVehicleOTAs(ctx, pool, uid)
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %v, err %v", got, err)
	}
	var back struct {
		Current struct{ Version string }
	}
	if err := json.Unmarshal(got["rv-1"], &back); err != nil || back.Current.Version != "2026.30.0" {
		t.Errorf("stored ota = %s, err %v", got["rv-1"], err)
	}
	var profile string
	if err := pool.QueryRowContext(ctx, `SELECT metadata->'profile'->>'keep' FROM vehicles WHERE user_id = $1`, uid).Scan(&profile); err != nil || profile != "me" {
		t.Errorf("profile metadata disturbed: %q, err %v", profile, err)
	}
	if o, _ := db.ListVehicleOTAs(ctx, pool, other); len(o) != 0 {
		t.Errorf("another user lists %v", o)
	}
	ids, err := db.ListUserRivianVehicleIDs(ctx, pool, uid)
	if err != nil || len(ids) != 1 || ids[0] != "rv-1" {
		t.Errorf("vehicle ids = %v, err %v", ids, err)
	}
}

// A vehicle Rivian won't give OTA details for (FORBIDDEN or unsupported) is
// marked, and no claim wins for a week even with a zero claim window; a week
// on one more try is allowed, and storing details clears the mark.
func TestVehicleOTAUnavailableBacksOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "ota-r2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-r2','R2')`, uid); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkOTAUnavailable(ctx, pool, uid, "rv-r2"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.ClaimOTAFetch(ctx, pool, uid, "rv-r2", 0); ok {
		t.Error("claimed a vehicle inside its unavailable back-off")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE vehicles SET metadata = jsonb_set(metadata, '{ota_unavailable_at}', to_jsonb((NOW() - interval '8 days')::text)) WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimOTAFetch(ctx, pool, uid, "rv-r2", 0); err != nil || !ok {
		t.Fatalf("claim a week on = %v, err %v", ok, err)
	}
	if err := db.SetVehicleOTA(ctx, pool, uid, "rv-r2", []byte(`{"current":{"version":"1"}}`)); err != nil {
		t.Fatal(err)
	}
	var marked bool
	if err := pool.QueryRowContext(ctx, `SELECT metadata ? 'ota_unavailable_at' FROM vehicles WHERE user_id = $1`, uid).Scan(&marked); err != nil || marked {
		t.Errorf("the unavailable mark survived a successful fetch: %v, err %v", marked, err)
	}
}
