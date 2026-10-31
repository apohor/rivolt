//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/singleton"
)

// Scenario 10 of the N-instance harness (replicas postmortem, infra #224): each singleton job runs on one pod at a time, and a
// message claimed through engage is delivered once however many pods race for it. Three pods are three pools on one database;
// the assertions sum over all pods, so they hold for any N.

func replicaPools(t *testing.T) (context.Context, []*sql.DB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	dsn := startPostgres(ctx, t)
	var pools []*sql.DB
	for i := 0; i < replicas; i++ {
		p, err := db.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("pod %d: db.Open: %v", i, err)
		}
		t.Cleanup(func() { _ = p.Close() })
		pools = append(pools, p)
	}
	return ctx, pools
}

// skipCounter counts the singleton helper's outcomes per job, as rivolt_singleton_runs_total does.
type skipCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func watchSingletons(t *testing.T) *skipCounter {
	c := &skipCounter{n: map[string]int{}}
	singleton.SetObserver(func(job, outcome string) { c.mu.Lock(); c.n[job+"/"+outcome]++; c.mu.Unlock() })
	t.Cleanup(func() { singleton.SetObserver(func(string, string) {}) })
	return c
}

func (c *skipCounter) get(job, outcome string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[job+"/"+outcome]
}

// onAllPods runs f on every pod at once and waits.
func onAllPods(pools []*sql.DB, f func(i int, p *sql.DB)) {
	var wg sync.WaitGroup
	for i, p := range pools {
		wg.Add(1)
		go func() { defer wg.Done(); f(i, p) }()
	}
	wg.Wait()
}

// holdLock takes a job's advisory lock on its own session, as a pod in the middle of the job does, and returns its release.
func holdLock(ctx context.Context, t *testing.T, pool *sql.DB, id int64) (release func()) {
	t.Helper()
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, id).Scan(&got); err != nil || !got {
		t.Fatalf("take lock %x: %v %v", id, got, err)
	}
	return func() {
		_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, id)
		_ = conn.Close()
	}
}

// The partition janitor and the pack-health backfill: while one pod is in the job every other pod skips its tick (nothing changes,
// each skip is counted); once it is out, pods racing for the job run it, each attempt is counted as ran or skipped, and the job's work
// is done.
func TestReplicasSingletonJobsRunOnOnePodAtATime(t *testing.T) {
	ctx, pools := replicaPools(t)
	skips := watchSingletons(t)
	pool := pools[0]

	// Janitor: a month the lookahead covers, dropped.
	var part string
	if err := pool.QueryRowContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition(now() + interval '3 months')`).Scan(&part); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `DROP TABLE `+part); err != nil {
		t.Fatal(err)
	}
	partExists := func() bool {
		var ok bool
		if err := pool.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, part).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// Backfill: an out-of-range sample the pass prunes.
	uid, err := db.EnsureUser(ctx, pool, "scn10-ph")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'rv-scn10')`, uid)
	exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source)
		SELECT $1, id, 'scn10-1', now() - interval '2 hours', now() - interval '1 hour', 'live' FROM vehicles WHERE rivian_vehicle_id = 'rv-scn10'`, uid)
	exec(`INSERT INTO vehicle_pack_health_samples (vehicle_id, charge_id, at, pack_kwh_effective, soc_delta_pct, energy_delivered_kwh, derate_active)
		SELECT c.vehicle_id, c.id, now(), 10, 50, 5, false FROM charges c WHERE c.external_id = 'scn10-1'`)
	sampleCount := func() (n int) {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicle_pack_health_samples`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tickJanitor := func(i int, p *sql.DB) { samples.NewPartitionJanitor(p).Tick(ctx) }
	tickBackfill := func(i int, p *sql.DB) { _, _ = packhealth.NewStore(p).BackfillAll(ctx) }

	// One pod is in both jobs: the other pods skip, and nothing changes.
	releaseJ := holdLock(ctx, t, pools[0], 0x72765f7061726a) // "rv_parj"
	releaseB := holdLock(ctx, t, pools[0], 0x72765f706b686c) // "rv_pkhl"
	onAllPods(pools[1:], tickJanitor)
	onAllPods(pools[1:], tickBackfill)
	if partExists() || sampleCount() != 1 {
		t.Fatalf("a job ran on a second pod while one held it: partition back=%v, samples=%d (want the seeded 1)", partExists(), sampleCount())
	}
	for _, job := range []string{"partition_janitor", "pack_health_backfill"} {
		if got := skips.get(job, "skipped"); got != replicas-1 {
			t.Errorf("%s: %d skips counted, want %d", job, got, replicas-1)
		}
		if got := skips.get(job, "ran"); got != 0 {
			t.Errorf("%s: %d runs counted while another pod held it", job, got)
		}
	}

	// Out of the jobs: every pod ticks at once. Each attempt is a run or a skip; at least one ran; the work is done.
	releaseJ()
	releaseB()
	onAllPods(pools, tickJanitor)
	onAllPods(pools, tickBackfill)
	for _, job := range []string{"partition_janitor", "pack_health_backfill"} {
		ran, skipped := skips.get(job, "ran"), skips.get(job, "skipped")-(replicas-1)
		if ran < 1 || ran+skipped != replicas {
			t.Errorf("%s: %d ran and %d skipped across %d pods racing, want at least one run and every attempt counted", job, ran, skipped, replicas)
		}
		if e := skips.get(job, "error"); e != 0 {
			t.Errorf("%s: %d lock errors", job, e)
		}
	}
	if !partExists() {
		t.Error("the janitor did not recreate the partition once the lock was free")
	}
	if sampleCount() != 0 {
		t.Error("the backfill did not prune the out-of-range sample once the lock was free")
	}
}

// A message claimed through engage is delivered once however many pods race for it: the home-change notice (kind home_changed,
// keyed by the hour) and the network-confirm push (kind network_confirm, keyed by the charge) are utility claims, and the claim is
// one INSERT ... ON CONFLICT DO NOTHING, so exactly one pod wins each and a different key is a different message.
func TestReplicasEngageClaimsDeliverOnce(t *testing.T) {
	ctx, pools := replicaPools(t)
	uid, err := db.EnsureUserVerified(ctx, pools[0], "scn10-push", "scn10@x.test", "scn10", true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		kind, key string
		ch        engage.Channel
	}{
		{"home_changed", "2026112012", engage.Email},
		{"network_confirm", "live_rv-test-1_c_1", engage.Push},
	} {
		var delivered atomic.Int32
		onAllPods(pools, func(i int, p *sql.DB) {
			msg := engage.Msg{User: uid, Kind: c.kind, Key: c.key, Channel: c.ch, Utility: true}
			claimed, _, err := engage.NewStore(p).Claim(ctx, msg, now, now.Add(-engage.BudgetWindow), engage.MonthlyBudget)
			if err != nil {
				t.Errorf("%s pod %d: %v", c.kind, i, err)
			}
			if claimed {
				delivered.Add(1)
			}
		})
		if delivered.Load() != 1 {
			t.Errorf("%s: %d pods claimed %q, want exactly 1", c.kind, delivered.Load(), c.key)
		}
		// The next hour / the next charge is a new message, and a utility claim costs no budget.
		next := c.key + "x"
		if claimed, _, err := engage.NewStore(pools[1]).Claim(ctx, engage.Msg{User: uid, Kind: c.kind, Key: next, Channel: c.ch, Utility: true}, now, now.Add(-engage.BudgetWindow), engage.MonthlyBudget); err != nil || !claimed {
			t.Errorf("%s: a different key was not claimable: %v %v", c.kind, claimed, err)
		}
	}
	if used, err := engage.NewStore(pools[2]).BudgetUsed(ctx, uid, now.Add(-engage.BudgetWindow)); err != nil || used != 0 {
		t.Errorf("utility claims used %d of the budget (%v), want 0", used, err)
	}
}
