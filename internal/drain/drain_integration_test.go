//go:build integration

package drain_test

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drain"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/testpg"
)

// TestCompute runs the fleet query over hand-built histories whose
// answers are worked out in the comments.
func TestCompute(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	dsn := testpg.DSN(ctx, t)
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	factory := samples.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	insert := func(user string, rows []samples.Sample) {
		uid, err := db.EnsureUser(ctx, pool, user)
		if err != nil {
			t.Fatal(err)
		}
		if err := factory.For(uid).InsertBulk(ctx, rows); err != nil {
			t.Fatalf("InsertBulk %s: %v", user, err)
		}
	}

	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	at := func(h float64) time.Time { return now.Add(time.Duration(h * float64(time.Hour))) }
	parked := func(vid string, ts time.Time, soc, odo float64, ps string) samples.Sample {
		return samples.Sample{VehicleID: vid, At: ts, BatteryLevelPct: soc, OdometerMi: odo,
			ShiftState: "P", ChargingState: "charger_disconnected", PowerState: ps, Source: "live"}
	}

	// rv-a: parked for the last 20 h, a row every 10 min, losing 0.1
	// SoC/h → 2.4 %/day over 20 parked h. Power state:
	//   [-20h,-18h) standby, [-18h,-10h) sleep (8 h), [-10h,-6h) standby,
	//   [-6h,-6h+30s) sleep (a 30 s quick re-wake), then standby to -4h,
	//   then asleep through now (not ended, not counted).
	// Idle-awake = 2 h + 4 h + (2 h - 30 s) over 20 h observed.
	// 7d: the same rows, but 20 h is under the 24 h minimum.
	var a []samples.Sample
	psA := func(h float64) string {
		switch {
		case h < -18:
			return "standby"
		case h < -10:
			return "sleep"
		case h < -6:
			return "standby"
		case h == -6:
			return "sleep"
		case h < -4:
			return "standby"
		default:
			return "sleep"
		}
	}
	for i := 0; i <= 120; i++ {
		h := -20 + float64(i)/6
		a = append(a, parked("rv-a", at(h), 80-0.1*(h+20), 1000, psA(h)))
	}
	q := at(-6).Add(30 * time.Second)
	a = append(a, parked("rv-a", q, 80-0.1*(14+30.0/3600), 1000, "standby"))
	insert("drain-a", a)

	// rv-b: 48 h, a row every 10 min, parked and asleep losing 0.05
	// SoC/h (1.2 %/day) except:
	//   [-40h,-39h) a drive (gear D, odometer and SoC moving): excluded;
	//   [-39h,-35h) charging (SoC rising): excluded, including the
	//   first parked pair after it;
	//   at -20h the SoC jumps +5 while parked (an unrecorded charge):
	//   that one pair is excluded.
	// Its only sleeps have no known start or no end, so none count.
	// No idle-awake time: awake only while driving or charging.
	var b []samples.Sample
	soc, odo := 70.0, 2000.0
	for i := 0; i <= 288; i++ {
		h := -50 + float64(i)/6
		s := parked("rv-b", at(h), soc, odo, "sleep")
		switch {
		case h >= -40 && h < -39:
			s.ShiftState, s.SpeedMph, s.PowerState = "D", 40, "go"
			odo += 5
			soc -= 1
		case h >= -39 && h < -35:
			s.ChargingState, s.PowerState = "charging_active", "ready"
			soc += 2
		case math.Abs(h-(-20)) < 1e-9:
			soc += 5
			s.BatteryLevelPct = soc
			soc -= 0.05 / 6
		default:
			soc -= 0.05 / 6
		}
		b = append(b, s)
	}
	// A row 8 days back is outside both windows.
	b = append(b, parked("rv-b", now.Add(-8*24*time.Hour), 99, 1900, "sleep"))
	insert("drain-b", b)

	// rv-d: 8 h parked and awake with no recorded gear or charging
	// state (NULL): still parked and not charging, so 24 awake h/day.
	var d []samples.Sample
	for i := 0; i <= 48; i++ {
		d = append(d, parked("rv-d", at(-9+float64(i)/6), 60, 500, "standby"))
	}
	insert("drain-d", d)
	// The samples store writes "" as ''; older and imported rows carry
	// NULL, which is the case the COALESCE covers.
	if _, err := pool.ExecContext(ctx, `
		UPDATE vehicle_state SET shift_state = NULL, charging_state = NULL
		 WHERE vehicle_id = (SELECT id FROM vehicles WHERE rivian_vehicle_id = 'rv-d')`); err != nil {
		t.Fatal(err)
	}

	// rv-c: nothing in the last 7 days → no stats at all.
	insert("drain-c", []samples.Sample{parked("rv-c", now.Add(-9*24*time.Hour), 50, 10, "sleep")})

	stats, err := drain.Compute(ctx, pool, now)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	got := map[string]drain.Stat{}
	for _, s := range stats {
		got[s.VehicleID+"/"+s.Window] = s
	}
	if len(stats) != 6 {
		t.Fatalf("stats = %d (%v), want 6 (rv-a, rv-b, rv-d; two windows)", len(stats), got)
	}
	near := func(name string, got *float64, want, tol float64) {
		t.Helper()
		if got == nil {
			t.Errorf("%s = nil, want %v", name, want)
			return
		}
		if math.Abs(*got-want) > tol {
			t.Errorf("%s = %v, want %v", name, *got, want)
		}
	}
	f := func(v float64) *float64 { return &v }

	// The row at exactly now is outside [now-7d, now), so rv-a's last
	// 10-min interval (asleep) isn't seen: 19 h 50 m parked and observed.
	a1 := got["rv-a/24h"]
	near("rv-a 24h parked h", f(a1.ParkedHours), 20-1.0/6, 1e-6)
	near("rv-a 24h drain", a1.DrainPctPerDay, 2.4, 1e-6)
	near("rv-a 24h awake", a1.IdleAwakeHPerDay, 24*(8-30.0/3600)/(20-1.0/6), 1e-6)
	if a1.Sleeps != 2 || a1.QuickRewakes != 1 {
		t.Errorf("rv-a 24h sleeps = %d, quick = %d; want 2, 1", a1.Sleeps, a1.QuickRewakes)
	}
	if a1.MedianSleep == nil || *a1.MedianSleep != (8*time.Hour+30*time.Second)/2 {
		t.Errorf("rv-a 24h median = %v, want 4h0m15s", a1.MedianSleep)
	}
	a7 := got["rv-a/7d"]
	if a7.DrainPctPerDay != nil || a7.IdleAwakeHPerDay != nil {
		t.Errorf("rv-a 7d: 20 h is under the 7d minimum, got %+v", a7)
	}
	if a7.Sleeps != 2 || a7.QuickRewakes != 1 {
		t.Errorf("rv-a 7d sleeps = %d, quick = %d; want 2, 1", a7.Sleeps, a7.QuickRewakes)
	}

	// rv-b 7d: parked pairs are 59 before the drive (9 h 50 m) and 198
	// after the charge minus the jump (32 h 50 m).
	b7 := got["rv-b/7d"]
	near("rv-b 7d parked h", f(b7.ParkedHours), 9+50.0/60+32+50.0/60, 1e-6)
	near("rv-b 7d drain", b7.DrainPctPerDay, 1.2, 1e-6)
	near("rv-b 7d observed h", f(b7.ObservedHours), 48, 1e-6)
	near("rv-b 7d awake", b7.IdleAwakeHPerDay, 0, 1e-9)
	if b7.Sleeps != 0 || b7.MedianSleep != nil {
		t.Errorf("rv-b 7d sleeps = %d, median = %v; want none", b7.Sleeps, b7.MedianSleep)
	}
	// rv-b 24h: the 133 pairs ending in [-24h, -2h], minus the jump: 22 h.
	b1 := got["rv-b/24h"]
	near("rv-b 24h parked h", f(b1.ParkedHours), 22, 1e-6)
	near("rv-b 24h drain", b1.DrainPctPerDay, 1.2, 1e-6)

	d1 := got["rv-d/24h"]
	near("rv-d 24h observed h", f(d1.ObservedHours), 8, 1e-6)
	near("rv-d 24h awake (NULL gear/charging state)", d1.IdleAwakeHPerDay, 24, 1e-6)
	near("rv-d 24h drain", d1.DrainPctPerDay, 0, 1e-9)
}

// Two replicas on one database: only the lock holder computes and exports; when
// it goes away the other takes over on its next tick.
func TestSingleRunnerAcrossReplicas(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := []*drain.Collector{drain.NewCollector(), drain.NewCollector()}
	ctxs := make([]context.Context, 2)
	cancels := make([]context.CancelFunc, 2)
	done := make([]chan struct{}, 2)
	for i := range cs {
		ctxs[i], cancels[i] = context.WithCancel(ctx)
		done[i] = make(chan struct{})
		go func(i int) { cs[i].Run(ctxs[i], pool, 100*time.Millisecond, logger); close(done[i]) }(i)
	}
	defer func() {
		for i := range cs {
			cancels[i]()
			<-done[i]
		}
	}()
	exporting := func() (n int, who int) {
		who = -1
		for i, c := range cs {
			if testutil.CollectAndCount(c) > 0 {
				n++
				who = i
			}
		}
		return
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if ok() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("one replica to export", func() bool { n, _ := exporting(); return n == 1 })
	// It stays one for several ticks: the loser never exports.
	leader := -1
	for i := 0; i < 15; i++ {
		n, who := exporting()
		if n > 1 {
			t.Fatalf("%d replicas export at once", n)
		}
		if n == 1 {
			if leader >= 0 && who != leader {
				t.Fatalf("leadership moved from %d to %d without the leader going away", leader, who)
			}
			leader = who
		}
		time.Sleep(40 * time.Millisecond)
	}
	if leader < 0 {
		t.Fatal("no replica exported")
	}
	// The leader goes away; the other takes over.
	cancels[leader]()
	<-done[leader]
	other := 1 - leader
	waitFor("the other replica to take over", func() bool { return testutil.CollectAndCount(cs[other]) > 0 })
}
