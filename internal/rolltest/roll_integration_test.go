//go:build integration && e2e_roll

// Package rolltest is the rollout test of the zero-impact-deploys plan (section 5): two pods run in one process on a real Postgres and a Redis,
// each with what a real pod has for a vehicle (its lease store in lead mode, the lease coordinator, a state monitor with the recorder, the Redis
// live-state store and the fence), against a fake feed that streams a drive and a charge. Then the pods are rolled the way a rolling update does
// it (a new pod comes up, the old one drains: MarkDraining, then the coordinator's Shutdown, the real SIGTERM path) and the test reads the
// result from the database: one drive row, one charge row, no split, and the longest silence in the samples.
//
// Not covered: the Rivian WebSocket and the REST calls around a subscribe (the feed answers at once, where Rivian takes seconds), the HTTP side
// of a drain, and the chart's preStop sleep. Both pods run in one process, so the Go clock is shared and the numbers are the floor of what a
// real roll shows, not the real figure; the preview version of this test is the one to read for that.
//
// The hard assertions are the ones that must hold today: no split and no lost row. The silence target (RIVOLT_ROLL_GAP_MAX, default 30 s, the
// plan's maximum) is a hard assertion too, and the p95 target of 12 s is only logged until the G1 fixes land. CI runs it as a job that does not
// block merges (.github/workflows/roll-e2e.yml).
//
// Run: go test -tags=integration,e2e_roll -count=1 -timeout=15m ./internal/rolltest
package rolltest

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/testpg"
)

// The vehicles: three on a drive, three on a charge, one user each, so six groups and both pods of a generation hold some.
var (
	driveVehicles  = []string{"roll-drive-1", "roll-drive-2", "roll-drive-3"}
	chargeVehicles = []string{"roll-charge-1", "roll-charge-2", "roll-charge-3"}
	allVehicles    = append(append([]string{}, driveVehicles...), chargeVehicles...)
)

func isDrive(vid string) bool { return strings.HasPrefix(vid, "roll-drive") }

// road is the feed both pods stream from: a drive and a charge that go on whatever pod is streaming them, as the real vehicle does.
type road struct{ t0 time.Time }

func (r road) frame(vid string, now time.Time) rivian.State {
	el := now.Sub(r.t0).Seconds()
	st := rivian.State{VehicleID: vid, At: now, PowerState: "go"}
	if isDrive(vid) {
		st.Gear, st.SpeedKph = "D", 90
		st.OdometerKm = 30000 + 90*el/3600
		st.BatteryLevelPct = 80 - el/120
		st.Latitude, st.Longitude = 35+el*0.0002, -97
		return st
	}
	st.Gear = "P"
	st.ChargerState, st.ChargerStatus, st.ChargerPowerKW = "charging_active", "chrgr_sts_connected_charging", 7.4
	st.BatteryLevelPct = 30 + el/200
	st.OdometerKm, st.Latitude, st.Longitude = 30000, 35.5, -97.5
	return st
}

func (r road) Stream(ctx context.Context, vid string, emit func(st rivian.State, catchingUp bool)) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		emit(r.frame(vid, time.Now()), false)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

type pod struct {
	name   string
	store  *leases.Store
	coord  *leases.Coordinator
	mons   map[string]*rivian.StateMonitor // by vehicle
	cancel context.CancelFunc
	done   chan struct{}
	fenced atomic.Int64
}

type world struct {
	t     *testing.T
	pool  *sql.DB
	rdb   *redis.Client
	users map[string]uuid.UUID // by vehicle
	road  road
	logr  *slog.Logger

	mu       sync.Mutex
	acquires map[string]int // by vehicle: how many times a pod took it
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := &world{t: t, pool: pool, rdb: rdb, users: map[string]uuid.UUID{}, road: road{t0: time.Now()},
		logr: slog.New(slog.NewTextHandler(io.Discard, nil)), acquires: map[string]int{}}
	for _, vid := range allVehicles {
		uid, err := db.EnsureUser(ctx, pool, "u-"+vid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2)`, uid, vid); err != nil {
			t.Fatal(err)
		}
		w.users[vid] = uid
	}
	return w
}

// activate gives every user a stored Rivian session, which makes their groups candidates. Done once both pods are up and registered, so
// they share the groups the way a fleet's pods do (a group with an open session is never shed, so a late second pod would get none).
func (w *world) activate() {
	w.t.Helper()
	for _, uid := range w.users {
		if _, err := w.pool.Exec(`INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00'::bytea, 'k')`, uid); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) podsRegistered(n int) bool {
	var c int
	_ = w.pool.QueryRow(`SELECT count(*) FROM lease_pods WHERE seen_at > now() - interval '10 seconds' AND NOT draining`).Scan(&c)
	return c >= n
}

// start brings up a pod of the given generation: lease store in lead mode, one monitor per user, the coordinator running.
func (w *world) start(name, generation string, rdb *redis.Client) *pod {
	w.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := leases.NewStore(w.pool, name)
	if err != nil {
		w.t.Fatal(err)
	}
	store.SetGroupLead(true)
	store.SetGeneration(generation)
	resolvers := db.NewVehicleResolverFactory(w.pool)
	sf, df, cf := samples.NewFactory(w.pool, resolvers), drives.NewFactory(w.pool, resolvers), charges.NewFactory(w.pool, resolvers)
	p := &pod{name: name, store: store, mons: map[string]*rivian.StateMonitor{}, cancel: cancel, done: make(chan struct{})}
	for vid, uid := range w.users {
		m := rivian.NewSourceMonitor(w.road, []rivian.Vehicle{{ID: vid, Model: "R1T"}}, w.logr)
		m.SetLiveStateStore(rivian.NewRedisLiveStateStore(rdb, uid.String(), "roll"))
		m.SetStores(sf.For(uid), df.For(uid), cf.For(uid))
		m.SetFence(store, func(string) { p.fenced.Add(1) })
		m.Start(ctx)
		p.mons[vid] = m
	}
	vehicles := func(context.Context) ([]string, error) { return allVehicles, nil }
	p.coord = leases.NewCoordinator(store, vehicles,
		func(v string) {
			w.mu.Lock()
			w.acquires[v]++
			w.mu.Unlock()
			p.mons[v].EnsureSubscribed(v)
		},
		func(v string) { p.mons[v].Unsubscribe(v) }, w.logr)
	p.coord.SetStreamingCheck(func(v string) bool { return p.mons[v].Subscribed(v) })
	p.coord.SetActivityCheck(func(v string) leases.Activity {
		if open, _ := p.mons[v].SessionActivity(v); open {
			return leases.ActivityBusy
		}
		return leases.ActivityAwake
	}, nil)
	go func() { defer close(p.done); _ = p.coord.Run(ctx) }()
	return p
}

// drain is the old pod's end of a rolling update: preStop marks it draining, SIGTERM runs the coordinator's shutdown (stop the
// subscriptions, wait, release the leases), then the process is gone.
func (p *pod) drain(preStop time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	p.coord.MarkDraining(ctx)
	time.Sleep(preStop)
	p.coord.Shutdown(ctx)
	p.cancel()
	<-p.done
}

// holders is which pod holds each vehicle's group ("" when none).
func (w *world) holders() map[string]string {
	out := map[string]string{}
	rows, err := w.pool.Query(`SELECT v.rivian_vehicle_id, COALESCE(l.pod_id, '') FROM vehicles v LEFT JOIN account_leases l ON l.group_id::text = v.user_id::text`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var vid, pod string
		if err := rows.Scan(&vid, &pod); err != nil {
			w.t.Fatal(err)
		}
		out[vid] = pod
	}
	return out
}

func (w *world) waitFor(within time.Duration, what string, ok func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	w.t.Fatalf("not within %s: %s", within, what)
}

func (w *world) count(table, vid string) int {
	var n int
	if err := w.pool.QueryRow(`SELECT count(*) FROM `+table+` WHERE user_id = $1`, w.users[vid]).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// longestSilence is the largest time between two consecutive samples of a vehicle, in seconds.
func (w *world) longestSilence(vid string) (secs float64, samples int) {
	err := w.pool.QueryRow(`SELECT COALESCE(max(gap), 0), count(*) FROM (SELECT extract(epoch FROM at - lag(at) OVER (ORDER BY at)) AS gap FROM vehicle_state WHERE user_id = $1) g`, w.users[vid]).Scan(&secs, &samples)
	if err != nil {
		w.t.Fatal(err)
	}
	return secs, samples
}

func maxGap() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("RIVOLT_ROLL_GAP_MAX"), 64); err == nil && v > 0 {
		return v
	}
	return 30
}

func TestRolloutKeepsEveryDriveAndCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	w := newWorld(t)
	roll(t, w, w.rdb)
}

// The same roll when Redis is empty at takeover (it restarted, or the snapshots expired): the generation 2 pods see no live-state snapshot.
// A charge still resumes from its open row; a drive has no such resume today (plan G2a), so this fails on the drives until that lands.
// It is the negative control of the test above, which would not notice a split it was blind to. Opt in with RIVOLT_ROLL_REDIS_LOSS=1.
func TestRolloutWithRedisLost(t *testing.T) {
	if testing.Short() || os.Getenv("RIVOLT_ROLL_REDIS_LOSS") != "1" {
		t.Skip("opt in with RIVOLT_ROLL_REDIS_LOSS=1")
	}
	w := newWorld(t)
	empty := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = empty.Close() })
	roll(t, w, empty)
}

func roll(t *testing.T, w *world, newRdb *redis.Client) {
	t.Helper()

	// Generation 1: both pods up and registered, then the groups appear and are shared between them, with a drive or charge open and
	// recording for each vehicle.
	a, b := w.start("pod-a-g1", "g1", w.rdb), w.start("pod-b-g1", "g1", w.rdb)
	pods := []*pod{a, b}
	defer func() {
		for _, p := range pods {
			p.cancel()
		}
	}()
	w.waitFor(30*time.Second, "both pods registered", func() bool { return w.podsRegistered(2) })
	w.activate()
	w.waitFor(90*time.Second, "every group held", func() bool {
		for _, h := range w.holders() {
			if h == "" {
				return false
			}
		}
		return true
	})
	w.waitFor(90*time.Second, "an open drive or charge per vehicle", func() bool {
		for _, v := range driveVehicles {
			if w.count("drives", v) != 1 {
				return false
			}
		}
		for _, v := range chargeVehicles {
			if w.count("charges", v) != 1 {
				return false
			}
		}
		return true
	})
	before := map[string]int{}
	w.mu.Lock()
	for v, n := range w.acquires {
		before[v] = n
	}
	w.mu.Unlock()
	held := map[string]int{}
	for _, h := range w.holders() {
		held[h]++
	}
	t.Logf("before the roll: groups held by pod %v", held)
	time.Sleep(15 * time.Second) // some history before the roll

	// The roll: a new pod first, then the old one drains; twice (maxSurge 1, maxUnavailable 0).
	for i, old := range []*pod{a, b} {
		n := w.start("pod-"+string(rune('a'+i))+"-g2", "g2", newRdb)
		pods = append(pods, n)
		time.Sleep(6 * time.Second) // the new pod registers and takes its turn
		old.drain(2 * time.Second)
		time.Sleep(20 * time.Second) // settle: every group must be streaming again
	}

	for v, h := range w.holders() {
		if h == "" || h == "pod-a-g1" || h == "pod-b-g1" {
			t.Errorf("%s is held by %q after the roll, want a generation 2 pod", v, h)
		}
	}
	for _, v := range driveVehicles {
		if n := w.count("drives", v); n != 1 {
			t.Errorf("%s: %d drive rows, want 1: the roll split the drive", v, n)
		}
	}
	for _, v := range chargeVehicles {
		if n := w.count("charges", v); n != 1 {
			t.Errorf("%s: %d charge rows, want 1: the roll split the charge", v, n)
		}
	}
	worst, moved := 0.0, 0
	w.mu.Lock()
	for _, v := range allVehicles {
		s, n := w.longestSilence(v)
		if s > worst {
			worst = s
		}
		// Moves in the roll: acquires after the initial one. Two is the minimum (each old pod's groups move once to a new pod).
		m := w.acquires[v] - before[v]
		if m > moved {
			moved = m
		}
		t.Logf("%s: %d samples, longest silence %.1f s, taken %d times in the roll", v, n, s, m)
	}
	w.mu.Unlock()
	if worst > maxGap() {
		t.Errorf("a vehicle was silent for %.1f s in the roll, the plan's maximum is %.0f s", worst, maxGap())
	}
	if worst > 12 {
		t.Logf("the longest silence %.1f s is over the 12 s target (G1 is not done)", worst)
	}
	if moved > 2 {
		t.Logf("a group moved %d times in a two-pod roll; the plan wants at most 1 per roll (G1c)", moved)
	}
	var fenced int64
	for _, p := range pods {
		fenced += p.fenced.Load()
	}
	t.Logf("writes refused by the fence across the roll: %d", fenced)
}
