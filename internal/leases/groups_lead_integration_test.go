//go:build integration

package leases_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/apohor/rivolt/internal/leases"
)

// Lead mode (M2): the group lease decides ownership, and the pods spread the groups. The pods run
// the passes the Coordinator runs, by hand, so each assertion can look at every pod.

func seedSessionGroups(ctx context.Context, t *testing.T, pool *sql.DB, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("sp-%02d", i)
		seedUserVehicles(ctx, t, pool, name, "veh-"+name)
		if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id)
			SELECT id, 'rivian.session', '\x00'::bytea, 'k' FROM users WHERE username = $1`, name); err != nil {
			t.Fatal(err)
		}
	}
}

func leadStore(t *testing.T, pool *sql.DB, name string) *leases.Store {
	t.Helper()
	s, err := leases.NewStore(pool, name)
	if err != nil {
		t.Fatal(err)
	}
	s.SetGroupLead(true)
	return s
}

// pass is one coordinator tick for the group lease: lead, then release what was shed.
func pass(ctx context.Context, t *testing.T, s *leases.Store) leases.GroupLead {
	t.Helper()
	r, err := s.LeadGroups(ctx)
	if err != nil {
		t.Fatalf("%s LeadGroups: %v", s.PodID(), err)
	}
	if len(r.Shed) > 1 {
		t.Errorf("%s shed %d groups in one pass", s.PodID(), len(r.Shed))
	}
	for _, g := range r.Shed {
		if err := s.ReleaseGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func heldBy(ctx context.Context, t *testing.T, pool *sql.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, r := range heldRows(ctx, t, pool) {
		out[r.pod]++
	}
	return out
}

func converge(ctx context.Context, t *testing.T, pods []*leases.Store, limit int) {
	t.Helper()
	for i := 0; i < limit; i++ {
		moved := false
		for _, p := range pods {
			r := pass(ctx, t, p)
			if len(r.Shed) > 0 {
				moved = true
			}
		}
		if !moved && i > 0 {
			return
		}
	}
}

func TestLead_SpreadsAcrossPods_3_5_2(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	const groups = 12
	seedSessionGroups(ctx, t, pool, groups)

	a, b, c := leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b"), leadStore(t, pool, "pod-c")
	// A starts alone and takes everything (the prod shape today).
	if r := pass(ctx, t, a); len(r.Held) != groups {
		t.Fatalf("a alone holds %d, want %d", len(r.Held), groups)
	}

	check := func(label string, pods []*leases.Store) {
		t.Helper()
		share := (groups+len(pods)-1)/len(pods) + 1
		by := heldBy(ctx, t, pool)
		total := 0
		for _, p := range pods {
			if by[p.PodID()] > share {
				t.Errorf("%s: %s holds %d, share %d (%v)", label, p.PodID(), by[p.PodID()], share, by)
			}
			if by[p.PodID()] == 0 {
				t.Errorf("%s: %s holds nothing (%v)", label, p.PodID(), by)
			}
			total += by[p.PodID()]
		}
		if total != groups {
			t.Errorf("%s: %d groups held, want %d (%v)", label, total, groups, by)
		}
	}

	pods := []*leases.Store{a, b, c}
	converge(ctx, t, pods, 30)
	check("3 pods", pods)

	d, e := leadStore(t, pool, "pod-d"), leadStore(t, pool, "pod-e")
	pods = append(pods, d, e)
	converge(ctx, t, pods, 40)
	check("5 pods", pods)

	// Scale down to 2: the three leave (a graceful stop releases everything), the rest take the groups.
	for _, p := range []*leases.Store{c, d, e} {
		if _, err := p.ReleaseAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	pods = []*leases.Store{a, b}
	converge(ctx, t, pods, 30)
	check("2 pods", pods)
}

func TestLead_DrainingPodsAcquireNothing_NewestGenerationFirst(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedSessionGroups(ctx, t, pool, 4)

	old1, old2, fresh := leadStore(t, pool, "pod-old1"), leadStore(t, pool, "pod-old2"), leadStore(t, pool, "pod-new")
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	pass(ctx, t, old1)
	pass(ctx, t, old2)
	t.Setenv("RIVOLT_POD_GENERATION", "g2")
	pass(ctx, t, fresh) // registers with g2; started later than the old pods
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET started_at = now() - interval '10 minutes' WHERE generation = 'g1'`); err != nil {
		t.Fatal(err)
	}
	converge(ctx, t, []*leases.Store{old1, old2, fresh}, 20)

	// old1 drains: it releases everything and must acquire nothing after.
	if err := old1.MarkDraining(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := old1.ReleaseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old1.MarkDraining(ctx); err != nil { // ReleaseAll removed its pod row; the flag is local
		t.Fatal(err)
	}
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	for i := 0; i < 3; i++ {
		if r := pass(ctx, t, old1); len(r.Held) != 0 {
			t.Fatalf("a draining pod holds %d groups", len(r.Held))
		}
	}

	// Free groups: the old generation waits 10 s for the new one, which takes them at once.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET pod_id = NULL, released_at = now() WHERE pod_id = 'pod-old2'`); err != nil {
		t.Fatal(err)
	}
	freed := len(heldRows(ctx, t, pool))
	pass(ctx, t, old2)
	if len(heldRows(ctx, t, pool)) > freed {
		t.Fatalf("old generation took a group released a moment ago")
	}
	t.Setenv("RIVOLT_POD_GENERATION", "g2")
	before := heldBy(ctx, t, pool)["pod-new"]
	pass(ctx, t, fresh)
	if heldBy(ctx, t, pool)["pod-new"] <= before {
		t.Fatalf("newest generation did not take the released groups")
	}
	// After 10 s of nobody taking it, the old generation may.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET pod_id = NULL, released_at = now() - interval '11 seconds' WHERE pod_id = 'pod-new'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIVOLT_POD_GENERATION", "g1")
	if r := pass(ctx, t, old2); len(r.Held) == 0 {
		t.Fatalf("old generation never took a group free for 11 s")
	}
}

// The re-key guard under concurrent passes: two pods run LeadGroups at the same time while a third
// holds the old key; neither takes the re-keyed row.
func TestLead_ReKeyGuardUnderConcurrentPasses(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	seedUserVehicles(ctx, t, pool, "ck-1", "ck-shared")
	seedUserVehicles(ctx, t, pool, "ck-2", "ck-own")
	var big, small string
	if err := pool.QueryRowContext(ctx, `SELECT (SELECT username FROM users WHERE username IN ('ck-1','ck-2') ORDER BY id DESC LIMIT 1), (SELECT username FROM users WHERE username IN ('ck-1','ck-2') ORDER BY id ASC LIMIT 1)`).Scan(&big, &small); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE vehicles SET user_id = (SELECT id FROM users WHERE username = $1) WHERE rivian_vehicle_id IN ('ck-shared','ck-own')`, big); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ck-1", "ck-2"} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id)
			SELECT id, 'rivian.session', '\x00'::bytea, 'k' FROM users WHERE username = $1`, u); err != nil {
			t.Fatal(err)
		}
	}
	a, b, c := leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b"), leadStore(t, pool, "pod-c")
	if r := pass(ctx, t, a); len(r.Held) != 1 {
		t.Fatalf("a holds %d groups, want 1", len(r.Held))
	}
	// Link the smaller id to the shared vehicle: one group now, keyed by the smaller id.
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) SELECT id, 'ck-shared' FROM users WHERE username = $1`, small); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 15; round++ {
		var wg sync.WaitGroup
		for _, s := range []*leases.Store{b, c, a} {
			wg.Add(1)
			go func(s *leases.Store) {
				defer wg.Done()
				if _, err := s.LeadGroups(ctx); err != nil {
					t.Errorf("%s: %v", s.PodID(), err)
				}
			}(s)
		}
		wg.Wait()
		// Overlap check: no user is covered by two pods' live rows.
		var overlaps int
		if err := pool.QueryRowContext(ctx, overlapSQL).Scan(&overlaps); err != nil {
			t.Fatal(err)
		}
		if overlaps != 0 {
			t.Fatalf("round %d: %d users covered by rows of different pods", round, overlaps)
		}
	}
	if n := len(heldRows(ctx, t, pool)); n != 1 {
		t.Fatalf("%d live rows after the re-key settled, want 1: %+v", n, heldRows(ctx, t, pool))
	}
}

// Users covered by the live rows of two different pods: a row covers its key user and, via the
// shared-vehicle link, the members of the group the key labels.
const overlapSQL = `
	WITH RECURSIVE edges AS (
	    SELECT DISTINCT v1.user_id AS a, v2.user_id AS b
	      FROM vehicles v1 JOIN vehicles v2 ON v2.rivian_vehicle_id = v1.rivian_vehicle_id
	), reach(a, b) AS (
	    SELECT a, b FROM edges UNION SELECT r.a, e.b FROM reach r JOIN edges e ON e.a = r.b
	), grp AS (SELECT a AS user_id, min(b::text) AS gid FROM reach GROUP BY a),
	cover AS (
	    SELECT l.pod_id, g.user_id FROM account_leases l JOIN grp g ON g.gid = l.group_id::text
	     WHERE l.pod_id IS NOT NULL AND l.expires_at > now()
	    UNION
	    SELECT l.pod_id, l.group_id FROM account_leases l
	     WHERE l.pod_id IS NOT NULL AND l.expires_at > now()
	)
	SELECT count(*) FROM (SELECT user_id FROM cover GROUP BY user_id HAVING count(DISTINCT pod_id) > 1) x`
