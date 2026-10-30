//go:build integration

package leases_test

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/google/uuid"
)

// seedOneGroup adds one single-user group with a vehicle and a Rivian session.
func seedOneGroup(ctx context.Context, t *testing.T, pool *sql.DB, name string) {
	t.Helper()
	seedUserVehicles(ctx, t, pool, name, "veh-"+name)
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id)
		SELECT id, 'rivian.session', '\x00'::bytea, 'k' FROM users WHERE username = $1`, name); err != nil {
		t.Fatal(err)
	}
}

// Over its share, a pod reports every group it holds in the shed order (highest id first), with
// the store's default pick first, so the coordinator can pass over a busy one.
func TestLead_ShedOrderListsEveryHeldGroup(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedSessionGroups(ctx, t, pool, 4)
	a, b := leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b")
	if r := pass(ctx, t, a); len(r.Held) != 4 || len(r.ShedOrder) != 0 {
		t.Fatalf("a alone: held %d, shed order %v", len(r.Held), r.ShedOrder)
	}
	pass(ctx, t, b) // b registers; every group is held
	r, err := a.LeadGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ShedOrder) != 4 || len(r.Shed) != 1 || r.Shed[0] != r.ShedOrder[0] {
		t.Fatalf("shed %v, order %v; want all 4 held, the default first", r.Shed, r.ShedOrder)
	}
	if !sort.SliceIsSorted(r.ShedOrder, func(i, j int) bool { return r.ShedOrder[i] > r.ShedOrder[j] }) {
		t.Errorf("shed order %v is not highest id first", r.ShedOrder)
	}
}

// AcquireGroups takes a free candidate group outside the share, never one another live pod holds
// or one that is not a candidate, and nothing on a draining pod.
func TestLead_AcquireGroupsForAnOpenSession(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedSessionGroups(ctx, t, pool, 2)
	a, b := leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b")
	pass(ctx, t, a) // a holds both groups
	pass(ctx, t, b)
	gid := func(user string) string {
		var id string
		if err := pool.QueryRowContext(ctx, `SELECT id::text FROM users WHERE username = $1`, user).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seedOneGroup(ctx, t, pool, "sp-02") // new and free
	free, heldByA := gid("sp-02"), gid("sp-00")

	got, err := b.AcquireGroups(ctx, []string{free, heldByA, uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[free] == 0 {
		t.Fatalf("acquired %v; want only the free group %s", got, free)
	}
	if rows := heldRows(ctx, t, pool); rows[free].pod != "pod-b" || rows[heldByA].pod != "pod-a" {
		t.Errorf("rows %+v; want %s on pod-b and %s still on pod-a", rows, free, heldByA)
	}
	// It renews like any other held group.
	if r := pass(ctx, t, b); r.Held[free] == 0 {
		t.Errorf("b's next pass does not hold %s: %v", free, r.Held)
	}

	if err := a.MarkDraining(ctx); err != nil {
		t.Fatal(err)
	}
	seedOneGroup(ctx, t, pool, "sp-03")
	if got, err := a.AcquireGroups(ctx, []string{gid("sp-03")}); err != nil || len(got) != 0 {
		t.Errorf("draining pod acquired %v, %v; want nothing", got, err)
	}
}
