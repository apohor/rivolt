//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
)

// The OTA details writes carry the group lease when the context has a token: the owner's claim,
// save, back-off and notices go through; a claim inside the window is "not won" (not a refusal);
// a stale epoch, another pod or a missing lease is refused with fence.ErrFenced and nothing
// changes. Without a token they run as before (TestVehicleOTAStoreAndClaim).
func TestOTAWrites_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "ota-fence")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-f','R1S')`, uid); err != nil {
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

	if ok, err := db.ClaimOTAFetch(owner, pool, uid, "rv-f", time.Hour); err != nil || !ok {
		t.Fatalf("owner claim = %v, %v", ok, err)
	}
	if ok, err := db.ClaimOTAFetch(owner, pool, uid, "rv-f", time.Hour); err != nil || ok {
		t.Fatalf("second claim inside the window = %v, %v; want not won, no error", ok, err)
	}
	if err := db.SetVehicleOTA(owner, pool, uid, "rv-f", json.RawMessage(`{"current":{"version":"1"}}`)); err != nil {
		t.Fatalf("owner save: %v", err)
	}
	if seeded, err := db.SeedOTANotices(owner, pool, uid, "rv-f", "1", ""); err != nil || !seeded {
		t.Fatalf("owner seed = %v, %v", seeded, err)
	}
	if fresh, err := db.ClaimOTANotice(owner, pool, uid, "rv-f", db.OTAInstalled, "2"); err != nil || !fresh {
		t.Fatalf("owner notice = %v, %v", fresh, err)
	}
	if err := db.MarkOTAUnavailable(owner, pool, uid, "rv-f"); err != nil {
		t.Fatalf("owner back-off: %v", err)
	}
	snapshot := func() (string, int) {
		var meta string
		var notices int
		if err := pool.QueryRowContext(ctx, `SELECT metadata::text FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = 'rv-f'`, uid).Scan(&meta); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM ota_notices WHERE user_id = $1`, uid).Scan(&notices); err != nil {
			t.Fatal(err)
		}
		return meta, notices
	}
	beforeMeta, beforeNotices := snapshot()

	for i, tok := range []fence.Token{
		{Group: uid.String(), Pod: "pod-a", Epoch: epoch - 1},
		{Group: uid.String(), Pod: "pod-b", Epoch: epoch},
		{Group: uuid.NewString(), Pod: "pod-a", Epoch: epoch},
	} {
		bad := fence.With(ctx, tok)
		for name, err := range map[string]error{
			"claim":   func() error { _, err := db.ClaimOTAFetch(bad, pool, uid, "rv-f", 0); return err }(),
			"save":    db.SetVehicleOTA(bad, pool, uid, "rv-f", json.RawMessage(`{"current":{"version":"9"}}`)),
			"backoff": db.MarkOTAUnavailable(bad, pool, uid, "rv-f"),
			"seed":    func() error { _, err := db.SeedOTANotices(bad, pool, uid, "rv-new", "9", ""); return err }(),
			"notice":  func() error { _, err := db.ClaimOTANotice(bad, pool, uid, "rv-f", db.OTAInstalled, "9"); return err }(),
		} {
			if !errors.Is(err, fence.ErrFenced) {
				t.Errorf("token %d, %s: %v; want fence.ErrFenced", i, name, err)
			}
		}
	}
	if m, n := snapshot(); m != beforeMeta || n != beforeNotices {
		t.Errorf("refused writes changed something:\n before %s (%d notices)\n after  %s (%d notices)", beforeMeta, beforeNotices, m, n)
	}
}
