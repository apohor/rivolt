//go:build integration

package milestones

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/testpg"
)

// The owner's milestone insert carries the group lease: a pod that lost the group cannot insert, so a
// zombie cannot flip `inserted` and cost the owner its notification.
func TestInsert_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "msfence")
	if err != nil {
		t.Fatal(err)
	}
	var veh uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'ms-veh','R1S') RETURNING id`, uid).Scan(&veh); err != nil {
		t.Fatal(err)
	}
	var epochA int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epochA); err != nil {
		t.Fatal(err)
	}
	store := NewStore(pool)
	row := func(key string) Row {
		return Row{User: uid, Vehicle: veh, Key: key, Status: "achieved", AchievedAt: time.Now(), Detail: Detail{}}
	}
	a := fence.With(ctx, fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epochA})

	if ok, err := store.Insert(a, row("k1")); err != nil || !ok {
		t.Fatalf("owner insert = %v %v", ok, err)
	}
	if ok, err := store.Insert(a, row("k1")); err != nil || ok {
		t.Fatalf("duplicate under a valid lease = %v %v, want false nil (not a refusal)", ok, err)
	}
	for name, bad := range map[string]fence.Token{
		"stale epoch": {Group: uid.String(), Pod: "pod-a", Epoch: epochA - 1},
		"other pod":   {Group: uid.String(), Pod: "pod-b", Epoch: epochA},
		"no lease":    {Group: uuid.NewString(), Pod: "pod-a", Epoch: epochA},
	} {
		if _, err := store.Insert(fence.With(ctx, bad), row("k2")); !errors.Is(err, fence.ErrFenced) {
			t.Errorf("%s: err = %v, want ErrFenced", name, err)
		}
	}
	// A steal: the old owner cannot insert the key; the new owner can, and gets `inserted`.
	var epochB int64
	if err := pool.QueryRowContext(ctx, `UPDATE account_leases SET pod_id = 'pod-b', epoch = nextval('account_lease_epoch'), expires_at = now() + interval '30 seconds' WHERE group_id = $1 RETURNING epoch`, uid).Scan(&epochB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(a, row("k2")); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner after a steal: %v, want ErrFenced", err)
	}
	b := fence.With(ctx, fence.Token{Group: uid.String(), Pod: "pod-b", Epoch: epochB})
	if ok, err := store.Insert(b, row("k2")); err != nil || !ok {
		t.Errorf("new owner's insert of the same key = %v %v, want true nil", ok, err)
	}
	// No token (catch-up, backfill): the plain insert.
	if ok, err := store.Insert(ctx, row("k3")); err != nil || !ok {
		t.Errorf("unguarded insert = %v %v", ok, err)
	}
}
