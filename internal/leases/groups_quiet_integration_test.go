//go:build integration

package leases_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/apohor/rivolt/internal/leases"
)

// The quiet shed: once the pod set and a pod's groups have stayed the same for a quiet period, the pod sheds down to ceil(groups/pods)
// instead of the fair share's ceil+1, and the group goes to a pod below that, never back to the pod that shed it.

// quietRounds is enough rounds of passes for several quiet periods (a round is one pass per pod).
const quietRounds = 13

// spread seeds n groups, gives pod i counts[i] of them straight in the table and registers every pod, so a test starts from any split.
func spread(ctx context.Context, t *testing.T, pool *sql.DB, n int, counts []int) []*leases.Store {
	t.Helper()
	seedSessionGroups(ctx, t, pool, n)
	stores := make([]*leases.Store, len(counts))
	for i := range counts {
		stores[i] = leadStore(t, pool, fmt.Sprintf("pod-%02d", i))
	}
	pass(ctx, t, stores[0]) // the first pod takes every group (share n+1)
	for i, s := range stores {
		if _, err := pool.ExecContext(ctx, `INSERT INTO lease_pods (pod_id, generation, started_at, seen_at, draining)
			VALUES ($1, '', now(), now(), false) ON CONFLICT (pod_id) DO UPDATE SET seen_at = now()`, s.PodID()); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	next := 0
	for i, c := range counts {
		for k := 0; k < c; k++ {
			if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET pod_id = $1 WHERE group_id = (
				SELECT group_id FROM account_leases ORDER BY group_id OFFSET $2 LIMIT 1)`, stores[i].PodID(), next); err != nil {
				t.Fatal(err)
			}
			next++
		}
	}
	return stores
}

// holders is how many groups each pod holds, from the table.
func holders(ctx context.Context, t *testing.T, pool *sql.DB, stores []*leases.Store) []int {
	t.Helper()
	out := make([]int, len(stores))
	for i, s := range stores {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM account_leases WHERE pod_id = $1`, s.PodID()).Scan(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// runRounds runs rounds of one pass per pod and returns every shed as (shedding pod index, group) and, for each, the pod that holds the group
// the round after.
func runRounds(ctx context.Context, t *testing.T, pool *sql.DB, stores []*leases.Store, rounds int) (sheds int, retaken int) {
	t.Helper()
	for r := 0; r < rounds; r++ {
		shedBy := map[string]string{} // group -> pod that shed it this round
		for _, s := range stores {
			res := pass(ctx, t, s)
			for _, g := range res.Shed {
				shedBy[g] = s.PodID()
				sheds++
			}
		}
		for g, pod := range shedBy {
			var holder sql.NullString
			if err := pool.QueryRowContext(ctx, `SELECT pod_id FROM account_leases WHERE group_id = $1::uuid`, g).Scan(&holder); err != nil {
				t.Fatal(err)
			}
			if holder.Valid && holder.String == pod {
				retaken++
			}
		}
	}
	return sheds, retaken
}

func maxOf(xs []int) (m int) {
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

func sumOf(xs []int) (s int) {
	for _, x := range xs {
		s += x
	}
	return s
}

// 3 groups on 2 pods: the pod holding all three sheds only after a quiet period, to 2/1.
func TestQuietShed_ThreeGroupsOnTwoPods(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	stores := spread(ctx, t, pool, 3, []int{3, 0})
	a := stores[0]
	// Not quiet yet: the fair share is 3, so nothing is shed for a whole quiet period.
	for i := 0; i < 12; i++ {
		if r := pass(ctx, t, a); len(r.Shed) != 0 {
			t.Fatalf("pass %d shed %v before the pod set had been quiet", i, r.Shed)
		}
	}
	sheds, retaken := runRounds(ctx, t, pool, stores, quietRounds*3)
	if got := holders(ctx, t, pool, stores); got[0] != 2 || got[1] != 1 {
		t.Fatalf("split = %v, want [2 1]", got)
	}
	if sheds != 1 || retaken != 0 {
		t.Errorf("sheds = %d, retaken = %d, want one shed and none taken back", sheds, retaken)
	}
}

// A change of the pod set restarts the quiet period: with one pod swapped for another just before it ends, the over-ceil pod waits again.
func TestQuietShed_PodSetChangeRestartsTheCount(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	stores := spread(ctx, t, pool, 6, []int{3, 2, 1}) // ceil 2, fair share 3: the first pod is over ceil only
	a := stores[0]
	for i := 0; i < 10; i++ {
		if r := pass(ctx, t, a); len(r.Shed) != 0 {
			t.Fatalf("pass %d shed %v before the quiet period", i, r.Shed)
		}
	}
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET pod_id = 'pod-swapped', seen_at = now() WHERE pod_id = $1`, stores[2].PodID()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if r := pass(ctx, t, a); len(r.Shed) != 0 {
			t.Fatalf("pass %d after the pod set changed shed %v", i, r.Shed)
		}
	}
	shed := false
	for i := 0; i < 3 && !shed; i++ {
		shed = len(pass(ctx, t, a).Shed) == 1
	}
	if !shed {
		t.Errorf("no shed once the pod set had been the same for a quiet period")
	}
}

// 12 groups on 5 pods from 4/3/3/1/1: nobody ends above ceil, nothing is shed twice or taken back, and once even, nothing moves again.
func TestQuietShed_ConvergesWithoutPingPong(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	stores := spread(ctx, t, pool, 12, []int{4, 3, 3, 1, 1})
	sheds, retaken := runRounds(ctx, t, pool, stores, quietRounds*4)
	got := holders(ctx, t, pool, stores)
	if sumOf(got) != 12 || maxOf(got) > 3 {
		t.Fatalf("split = %v, want 12 groups and none above 3", got)
	}
	if sheds != 1 || retaken != 0 {
		t.Errorf("sheds = %d, retaken = %d, want one shed and none taken back", sheds, retaken)
	}
	if more, _ := runRounds(ctx, t, pool, stores, quietRounds*8); more != 0 {
		t.Errorf("%d sheds after the split was even", more)
	}
}

// Twenty pods (design for N replicas): 25 groups held by five of them end at most 2 each.
func TestQuietShed_TwentyPods(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	counts := make([]int, 20)
	for i := 0; i < 5; i++ {
		counts[i] = 5
	}
	stores := spread(ctx, t, pool, 25, counts)
	sheds, retaken := runRounds(ctx, t, pool, stores, quietRounds*5)
	got := holders(ctx, t, pool, stores)
	if sumOf(got) != 25 || maxOf(got) > 2 {
		t.Fatalf("split = %v, want 25 groups and none above 2", got)
	}
	if want := 5 * 3; sheds != want || retaken != 0 {
		t.Errorf("sheds = %d, retaken = %d, want %d and none taken back", sheds, retaken, want)
	}
	if more, _ := runRounds(ctx, t, pool, stores, quietRounds*3); more != 0 {
		t.Errorf("%d sheds after the split was even", more)
	}
}

// One pod restarting (its groups released, a new pod id in its place) moves nothing on the other pods.
func TestQuietShed_OnePodRestartMovesNothingElse(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	stores := spread(ctx, t, pool, 6, []int{2, 2, 2})
	before := func(s *leases.Store) string {
		var ids sql.NullString
		if err := pool.QueryRowContext(ctx, `SELECT string_agg(group_id::text, ',' ORDER BY group_id) FROM account_leases WHERE pod_id = $1`, s.PodID()).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids.String
	}
	a0, b0 := before(stores[0]), before(stores[1])
	// The third pod goes: its groups are released and its row removed; a new pod takes its place.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET pod_id = NULL, released_at = statement_timestamp() WHERE pod_id = $1`, stores[2].PodID()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM lease_pods WHERE pod_id = $1`, stores[2].PodID()); err != nil {
		t.Fatal(err)
	}
	fresh := leadStore(t, pool, "pod-restarted")
	if _, err := pool.ExecContext(ctx, `INSERT INTO lease_pods (pod_id, generation, started_at, seen_at, draining)
		VALUES ($1, '', now(), now(), false)`, fresh.PodID()); err != nil {
		t.Fatal(err)
	}
	all := []*leases.Store{stores[0], stores[1], fresh}
	if sheds, _ := runRounds(ctx, t, pool, all, quietRounds*3); sheds != 0 {
		t.Errorf("%d sheds after one pod restarted", sheds)
	}
	if a1, b1 := before(stores[0]), before(stores[1]); a1 != a0 || b1 != b0 {
		t.Errorf("the other pods' groups changed: %q -> %q, %q -> %q", a0, a1, b0, b1)
	}
	if got := holders(ctx, t, pool, all); got[2] != 2 {
		t.Errorf("restarted pod holds %d, want its 2 back", got[2])
	}
}

// While a rollout is on (a pod draining, or two pod generations live) the quiet rule sheds nothing, however long the pod set has been the
// same, and a group released then goes by the fair share, not the steady one.
func TestQuietShed_NothingDuringARollout(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	stores := spread(ctx, t, pool, 6, []int{3, 2, 1}) // the first pod is over ceil only
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET generation = 'g2' WHERE pod_id = $1`, stores[2].PodID()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3*quietRounds; i++ {
		if r := pass(ctx, t, stores[0]); len(r.Shed) != 0 {
			t.Fatalf("pass %d shed %v with two generations live", i, r.Shed)
		}
	}
}

// release frees pod's groups as if it had released them age ago.
func release(ctx context.Context, t *testing.T, pool *sql.DB, pod string, age string) {
	t.Helper()
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET pod_id = NULL, released_at = statement_timestamp() - $2::interval WHERE pod_id = $1`, pod, age); err != nil {
		t.Fatal(err)
	}
}

// A row released a moment ago is kept for a pod below the even split, so a pod already at it leaves it; once no pod with room has taken it
// for longer than the live window, the pod takes it up to the fair share, so a group is never left unowned.
func TestQuietShed_FreshReleaseWaitsThenAnyPodTakesIt(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	stores := spread(ctx, t, pool, 3, []int{2, 1}) // even split is 2, fair share 3
	a, b := stores[0], stores[1]
	release(ctx, t, pool, b.PodID(), "0 seconds")
	if r := pass(ctx, t, a); len(r.Held) != 2 {
		t.Fatalf("pod at the even split took a row released just now: holds %d", len(r.Held))
	}
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET released_at = statement_timestamp() - interval '20 seconds' WHERE pod_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	if r := pass(ctx, t, a); len(r.Held) != 3 {
		t.Errorf("a row nobody with room took in 20 s stayed unowned: pod holds %d, want 3", len(r.Held))
	}
}

// In a rollout nothing is held back: a group released while two generations are live goes to the pod at the even split at once.
func TestQuietShed_ReleaseDuringARolloutIsNotHeldBack(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	stores := spread(ctx, t, pool, 3, []int{2, 1})
	a, b := stores[0], stores[1]
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET generation = 'g2', started_at = now() - interval '1 hour' WHERE pod_id = $1`, b.PodID()); err != nil {
		t.Fatal(err)
	}
	release(ctx, t, pool, b.PodID(), "0 seconds")
	if r := pass(ctx, t, a); len(r.Held) != 3 {
		t.Errorf("pod holds %d after the other generation released its group, want 3", len(r.Held))
	}
}
