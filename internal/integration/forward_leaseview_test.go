//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
)

type fakeLocal struct {
	tokens []leases.GroupToken
	past   bool
}

func (f fakeLocal) GroupTokens() []leases.GroupToken { return f.tokens }
func (f fakeLocal) PastLocalDeadline() bool          { return f.past }

// The forwarder's lease view against Postgres: a live row is its owner at its epoch; a released row, an expired row and a missing row
// are no owner (the database's clock decides, not the pod's); a read is cached for a moment; Held and PastDeadline come from the local
// store.
func TestForwardLeaseView(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	live, released, expired, missing := uuid.New().String(), uuid.New().String(), uuid.New().String(), uuid.New().String()
	exec(`INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at) VALUES ($1,'pod-a',7,now(),now(), now() + interval '30 seconds')`, live)
	exec(`INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at, released_at) VALUES ($1,NULL,3,now(),now(), now() + interval '30 seconds', now())`, released)
	exec(`INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at) VALUES ($1,'pod-b',4,now() - interval '2 minutes',now() - interval '2 minutes', now() - interval '1 minute')`, expired)

	local := fakeLocal{tokens: []leases.GroupToken{{Group: live, Epoch: 7}}}
	v := forward.NewPGLeaseView(pool, local)
	owner := func(g string) forward.LeaseOwner {
		t.Helper()
		o, err := v.Owner(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := owner(live); !o.Found || o.Pod != "pod-a" || o.Epoch != 7 {
		t.Errorf("live row: %+v, want pod-a at epoch 7", o)
	}
	for name, g := range map[string]string{"released": released, "expired": expired, "missing": missing} {
		if o := owner(g); o.Found {
			t.Errorf("%s row must be no owner, got %+v", name, o)
		}
	}
	// Cached for a moment: a change in the row is not seen at once (the owner re-checks its own lease before it runs anything).
	exec(`UPDATE account_leases SET pod_id = 'pod-z', epoch = 8 WHERE group_id = $1`, live)
	if o := owner(live); o.Pod != "pod-a" {
		t.Errorf("a read is cached for a moment, got %+v", o)
	}
	// A fresh view sees the new row.
	if o, err := forward.NewPGLeaseView(pool, local).Owner(ctx, live); err != nil || o.Pod != "pod-z" || o.Epoch != 8 {
		t.Errorf("a fresh view: %+v %v, want pod-z at epoch 8", o, err)
	}
	if e, ok := v.Held(live); !ok || e != 7 {
		t.Errorf("held: %d %v", e, ok)
	}
	if _, ok := v.Held(missing); ok {
		t.Error("a group the local store does not hold")
	}
	if v.PastDeadline() {
		t.Error("not past the deadline")
	}
	local.past = true
	if !forward.NewPGLeaseView(pool, local).PastDeadline() {
		t.Error("past the deadline is passed through")
	}
}
