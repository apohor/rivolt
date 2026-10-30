//go:build integration

package engage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/testpg"
)

// The claim insert of an owner-triggered send carries the group lease: a pod that lost the group
// cannot claim, a duplicate is still a duplicate, and the new owner can claim the same key.
func TestClaimAndSkip_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "engagefence")
	if err != nil {
		t.Fatal(err)
	}
	store := engage.NewStore(pool)
	var epochA int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epochA); err != nil {
		t.Fatal(err)
	}
	tokA := fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epochA}
	gctx := fence.With(ctx, tokA)
	now := time.Now()
	since := now.Add(-engage.BudgetWindow)
	msg := func(key string) engage.Msg {
		return engage.Msg{User: uid, Kind: "fence-test", Key: key, Channel: engage.Push}
	}
	rows := func() int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM engagement_sends WHERE user_id = $1`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The owner claims; a repeat is a duplicate, not a refusal.
	if ok, over, err := store.Claim(gctx, msg("a"), now, since, 4); err != nil || !ok || over {
		t.Fatalf("owner claim = %v %v %v", ok, over, err)
	}
	if ok, over, err := store.Claim(gctx, msg("a"), now, since, 4); err != nil || ok || over {
		t.Fatalf("duplicate claim under a valid lease = %v %v %v, want false false nil", ok, over, err)
	}
	// The budget is a skip, not a refusal.
	if ok, over, err := store.Claim(gctx, msg("b"), now, since, 1); err != nil || ok || !over {
		t.Fatalf("budget-exhausted claim = %v %v %v, want false true nil", ok, over, err)
	}
	if rows() != 1 {
		t.Fatalf("%d rows, want 1", rows())
	}

	// Stale epoch, other pod, no lease: refused, nothing written.
	for name, bad := range map[string]fence.Token{
		"stale epoch": {Group: tokA.Group, Pod: "pod-a", Epoch: epochA - 1},
		"other pod":   {Group: tokA.Group, Pod: "pod-b", Epoch: epochA},
		"no lease":    {Group: uuid.NewString(), Pod: "pod-a", Epoch: epochA},
	} {
		if _, _, err := store.Claim(fence.With(ctx, bad), msg("c"), now, since, 4); !errors.Is(err, fence.ErrFenced) {
			t.Errorf("claim, %s: err = %v, want ErrFenced", name, err)
		}
		if _, err := store.RecordSkip(fence.With(ctx, bad), uid, "fence-test", "s", engage.Push, engage.StatusEmpty, now); !errors.Is(err, fence.ErrFenced) {
			t.Errorf("skip, %s: err = %v, want ErrFenced", name, err)
		}
	}
	if rows() != 1 {
		t.Fatalf("a refused claim wrote a row: %d rows", rows())
	}

	// A steal: the old owner cannot claim; the new owner claims the very key the old one was refused.
	var epochB int64
	if err := pool.QueryRowContext(ctx, `UPDATE account_leases SET pod_id = 'pod-b', epoch = nextval('account_lease_epoch'), expires_at = now() + interval '30 seconds' WHERE group_id = $1 RETURNING epoch`, uid).Scan(&epochB); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Claim(gctx, msg("d"), now, since, 4); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner after a steal: err = %v, want ErrFenced", err)
	}
	nctx := fence.With(ctx, fence.Token{Group: tokA.Group, Pod: "pod-b", Epoch: epochB})
	if ok, _, err := store.Claim(nctx, msg("d"), now, since, 4); err != nil || !ok {
		t.Errorf("new owner's claim of the same key = %v, %v", ok, err)
	}
	if ok, err := store.RecordSkip(nctx, uid, "fence-test", "s", engage.Push, engage.StatusEmpty, now); err != nil || !ok {
		t.Errorf("new owner's skip = %v, %v", ok, err)
	}
	// No token (jobs, imports): the plain statements, unchanged.
	if ok, _, err := store.Claim(ctx, msg("e"), now, since, 4); err != nil || !ok {
		t.Errorf("unguarded claim = %v, %v", ok, err)
	}
}
