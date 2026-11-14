//go:build integration

package leases_test

import (
	"context"
	"testing"

	"github.com/apohor/rivolt/internal/leases"
)

// Scenario 11 of the N-instance harness (Before more than 2 replicas, 5): three pods spread the groups, the pod holding the most is killed
// (it stops renewing and releases nothing), and the two survivors take every one of its groups once the 30 s lease has run out, at a higher
// epoch, with no group ever held by two pods and the dead pod's tokens refused. The 30 s is moved back on the row instead of waited for.
func TestLead_ScenarioKilledHolderThreePods(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	const groups = 30
	seedSessionGroups(ctx, t, pool, groups)
	pods := []*leases.Store{leadStore(t, pool, "pod-a"), leadStore(t, pool, "pod-b"), leadStore(t, pool, "pod-c")}
	converge(ctx, t, pods, 40)
	by := heldBy(ctx, t, pool)
	var victim *leases.Store
	for _, p := range pods {
		if victim == nil || by[p.PodID()] > by[victim.PodID()] {
			victim = p
		}
	}
	if by[victim.PodID()] == 0 {
		t.Fatalf("nobody holds anything: %v", by)
	}
	survivors := []*leases.Store{}
	for _, p := range pods {
		if p != victim {
			survivors = append(survivors, p)
		}
	}
	oldEpoch := map[string]int64{}
	for g, r := range heldRows(ctx, t, pool) {
		if r.pod == victim.PodID() {
			oldEpoch[g] = r.epoch
		}
	}

	// Every pod's tokens are valid in the database, and no group is in two pods' tokens.
	valid := func(label string, ps []*leases.Store) {
		t.Helper()
		seen := map[string]string{}
		for _, p := range ps {
			for _, tok := range p.GroupTokens() {
				if other, dup := seen[tok.Group]; dup {
					t.Errorf("%s: group %s held by %s and %s", label, tok.Group, other, p.PodID())
				}
				seen[tok.Group] = p.PodID()
				var ok bool
				if err := pool.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid, $2, $3)`, tok.Group, p.PodID(), tok.Epoch).Scan(&ok); err != nil {
					t.Fatal(err)
				}
				if !ok {
					t.Errorf("%s: %s's token for %s (epoch %d) is not accepted", label, p.PodID(), tok.Group, tok.Epoch)
				}
			}
		}
	}
	valid("before the kill", pods)

	// The victim is killed: no more passes from it. While its lease is live the survivors take nothing of its.
	for i := 0; i < 3; i++ {
		for _, p := range survivors {
			pass(ctx, t, p)
		}
		for g := range oldEpoch {
			if r := heldRows(ctx, t, pool)[g]; r.pod != victim.PodID() {
				t.Fatalf("pass %d: a live group of %s moved to %q before its lease ran out", i, victim.PodID(), r.pod)
			}
		}
	}

	// 30 s pass without a renew from the victim (only its rows move back). Its pod registration is as old, so the survivors no longer
	// count it as live (podLiveWindow is 15 s) and each one's fair share is half of the groups, not a third.
	if _, err := pool.ExecContext(ctx, `UPDATE account_leases SET expires_at = now() - interval '1 second', renewed_at = now() - interval '31 seconds' WHERE pod_id = $1`, victim.PodID()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE lease_pods SET seen_at = now() - interval '31 seconds' WHERE pod_id = $1`, victim.PodID()); err != nil {
		t.Fatal(err)
	}

	// The bar: the first passes after the expiry (at most 20 acquires a pass, so a few for a big holder) hold every orphaned group.
	const barPasses = 4
	taken := 0
	for i := 0; i < barPasses; i++ {
		for _, p := range survivors {
			pass(ctx, t, p)
		}
		valid("after takeover pass", survivors)
		rows := heldRows(ctx, t, pool)
		taken = 0
		for g, e := range oldEpoch {
			if r := rows[g]; r.pod != "" && r.pod != victim.PodID() {
				if r.epoch <= e {
					t.Errorf("group %s taken at epoch %d, not above %d", g, r.epoch, e)
				}
				taken++
			}
		}
		if taken == len(oldEpoch) {
			break
		}
	}
	if taken != len(oldEpoch) {
		t.Fatalf("%d of %d orphaned groups taken within %d passes", taken, len(oldEpoch), barPasses)
	}
	if n := len(heldRows(ctx, t, pool)); n != groups {
		t.Errorf("%d groups held, want %d", n, groups)
	}

	// The victim wakes: its old tokens are refused and it renews none of the rows now held by others.
	for g, e := range oldEpoch {
		var ok bool
		if err := pool.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid, $2, $3)`, g, victim.PodID(), e).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("the killed pod's token for %s is still accepted", g)
		}
	}
	woke := pass(ctx, t, victim)
	for g := range oldEpoch {
		if _, held := woke.Held[g]; held {
			t.Errorf("the woken pod holds %s again", g)
		}
	}
	if len(woke.Lost) != len(oldEpoch) {
		t.Errorf("woken pod reports %d groups lost, want %d", len(woke.Lost), len(oldEpoch))
	}
}
