//go:build integration

package leases_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/testpg"
)

// A linked account with no vehicle yet (a stored Rivian session, no vehicles row) has no group, so no lease and no owner, until user-level groups
// are on (RIVOLT_OWNER_DISCOVERY): then it is a group of one, a candidate like any other, held by exactly one pod, and when its vehicles are
// stored the same group id keeps covering it, so the owner carries on. Users with vehicles are grouped exactly as before in both modes.
func TestVehiclelessLinkedUserIsAGroupOnlyWithUserLevelGroups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	t.Cleanup(func() { leases.SetUserLevelGroups(false) })

	seedUserVehicles(ctx, t, pool, "with-vehicle", "veh-with-1")
	nobody, err := db.EnsureUser(ctx, pool, "no-vehicle-yet")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"with-vehicle", "no-vehicle-yet"} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id)
			SELECT id, 'rivian.session', '\x00'::bytea, 'k' FROM users WHERE username = $1`, u); err != nil {
			t.Fatal(err)
		}
	}
	podA, podB := leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b")

	// Off (the default): only the user with a vehicle is a group.
	leases.SetUserLevelGroups(false)
	converge(ctx, t, []*leases.Store{podA, podB}, 6)
	if held := heldRows(ctx, t, pool); len(held) != 1 {
		t.Fatalf("user-level groups off: %d groups held, want 1 (the user with a vehicle): %v", len(held), held)
	}
	if _, ok := heldRows(ctx, t, pool)[nobody.String()]; ok {
		t.Fatal("a vehicle-less user was a group with user-level groups off")
	}

	// On: the vehicle-less linked user is a group of one with its own id, held by exactly one pod.
	leases.SetUserLevelGroups(true)
	converge(ctx, t, []*leases.Store{podA, podB}, 10)
	held := heldRows(ctx, t, pool)
	row, ok := held[nobody.String()]
	if !ok || len(held) != 2 {
		t.Fatalf("user-level groups on: held %v, want the vehicle-less user's group (id = the user id) and the other one", held)
	}
	owners := 0
	for _, s := range []*leases.Store{podA, podB} {
		for _, g := range s.GroupTokens() {
			if g.Group == nobody.String() {
				owners++
			}
		}
	}
	// The owner reports the user as one of its own, with the group's token.
	for _, s := range []*leases.Store{podA, podB} {
		hs, err := s.HeldGroupUsers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, h := range hs {
			if h.User == nobody {
				found = true
				if h.Token.Group != nobody.String() || h.Token.Pod != s.PodID() || h.Token.Epoch != row.epoch {
					t.Errorf("token %+v, want group %s, pod %s, epoch %d", h.Token, nobody, s.PodID(), row.epoch)
				}
			}
		}
		if found != (s.PodID() == row.pod) {
			t.Errorf("%s reports the vehicle-less user as its own = %v, but the row says %q holds it", s.PodID(), found, row.pod)
		}
	}
	if owners != 1 || row.pod == "" {
		t.Fatalf("the vehicle-less group has %d owners (row says %q), want exactly one", owners, row.pod)
	}

	// Its vehicles get stored: still one group with the same id, still the same owner and epoch (the owner carries on).
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'veh-found-1')`, nobody); err != nil {
		t.Fatal(err)
	}
	converge(ctx, t, []*leases.Store{podA, podB}, 6)
	after := heldRows(ctx, t, pool)[nobody.String()]
	if after.pod != row.pod || after.epoch != row.epoch {
		t.Errorf("after the vehicles were stored the group is held by %q at %d, was %q at %d", after.pod, after.epoch, row.pod, row.epoch)
	}
}
