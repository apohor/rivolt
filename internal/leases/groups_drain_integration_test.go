//go:build integration

package leases_test

import (
	"context"
	"testing"
)

// A draining pod releases its groups; the newest-generation successor takes every one of them in its next passes and sheds none
// (the #1757 rollout rule holds while the drain runs), however many groups arrive.
func TestDrain_SuccessorTakesEveryReleasedGroupWithNoShed(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	stores := spread(ctx, t, pool, 8, []int{8, 0})
	old, next := stores[0], stores[1]
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET generation = 'g2', started_at = now() - interval '1 hour' WHERE pod_id = $1`, next.PodID()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET draining = true WHERE pod_id = $1`, old.PodID()); err != nil {
		t.Fatal(err)
	}
	release(ctx, t, pool, old.PodID(), "0 seconds") // the drain handed all 8 over
	for i := 0; i < 3; i++ {
		if r := pass(ctx, t, next); len(r.Shed) != 0 {
			t.Fatalf("pass %d: the successor shed %v while the old pod drained", i, r.Shed)
		}
	}
	if got := holders(ctx, t, pool, stores); got[1] != 8 {
		t.Errorf("holders %v, want the successor to hold all 8", got)
	}
}

// Twenty pods roll one at a time: each one's groups go to the others and nothing is left unowned or shed on the way.
func TestDrain_TwentyPodsRollWithoutShed(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	counts := make([]int, 20)
	for i := range counts {
		counts[i] = 2
	}
	stores := spread(ctx, t, pool, 40, counts)
	for i := 0; i < 20; i++ {
		if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET draining = true WHERE pod_id = $1`, stores[i].PodID()); err != nil {
			t.Fatal(err)
		}
		release(ctx, t, pool, stores[i].PodID(), "0 seconds")
		rest := stores[i+1:]
		if len(rest) == 0 {
			break
		}
		if sheds, _ := runRounds(ctx, t, pool, rest, 3); sheds != 0 {
			t.Fatalf("pod %d draining: %d sheds on the others", i, sheds)
		}
		var unowned int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM account_leases WHERE pod_id IS NULL`).Scan(&unowned); err != nil {
			t.Fatal(err)
		}
		if unowned != 0 {
			t.Fatalf("pod %d draining: %d groups left unowned", i, unowned)
		}
	}
}
