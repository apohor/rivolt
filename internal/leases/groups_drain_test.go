package leases

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// drainHandStore is a draining lead-mode store: a released group leaves the held set, as it does in the table.
type drainHandStore struct{ *leaderStore }

func (d *drainHandStore) ReleaseGroup(ctx context.Context, g string) error {
	d.lmu.Lock()
	delete(d.res.Held, g)
	d.lmu.Unlock()
	return d.leaderStore.ReleaseGroup(ctx, g)
}

// drainRig holds one vehicle per group; kind says "drive" or "charge" for a busy one.
type drainRig struct {
	ls    *drainHandStore
	c     *Coordinator
	act   map[string]Activity
	kind  map[string]string
	moves []string
	now   time.Time
}

func newDrainRig(t *testing.T, groups int, perPass int, wait time.Duration) *drainRig {
	t.Helper()
	vg, held, vids := map[string]string{}, map[string]int64{}, []string{}
	for i := 0; i < groups; i++ {
		g, v := fmt.Sprintf("g%02d", i), fmt.Sprintf("v%02d", i)
		vg[v], held[g] = g, 1
		vids = append(vids, v)
	}
	ls := &drainHandStore{&leaderStore{fakeStore: newFakeStore(), vg: vg, res: GroupLead{Held: held}}}
	r := &drainRig{ls: ls, act: map[string]Activity{}, kind: map[string]string{}, now: time.Unix(1_000_000, 0)}
	r.c = newLeadCoord(ls.leaderStore, vids)
	r.c.store = ls
	r.c.now = func() time.Time { return r.now }
	r.c.SetActivityCheck(func(v string) Activity { return r.act[v] }, nil)
	r.c.SetHandoffObserver(func(v string) string { return r.kind[v] }, nil, nil)
	r.c.SetDrain(perPass, wait, func(reason string) { r.moves = append(r.moves, reason) })
	r.c.reconcile(context.Background()) // acquires the vehicles
	ls.lmu.Lock()
	ls.res.Draining = true
	ls.log = nil
	ls.lmu.Unlock()
	return r
}

func (r *drainRig) pass() []string {
	r.ls.lmu.Lock()
	r.ls.log = nil
	r.ls.lmu.Unlock()
	r.moves = nil
	r.c.reconcile(context.Background())
	sort.Strings(r.moves) // the releases run in order, the count order is not what is under test
	var out []string
	for _, call := range r.ls.calls() {
		var g string
		if _, err := fmt.Sscanf(call, "releaseGroup:%s", &g); err == nil {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

func (r *drainRig) held() int {
	r.ls.lmu.Lock()
	defer r.ls.lmu.Unlock()
	return len(r.ls.res.Held)
}

// Asleep groups go first, then awake, then charging; a drive is never handed over before the cap; the per-pass count is a value.
func TestDrain_IdleFirstDriveNeverBeforeTheCap(t *testing.T) {
	r := newDrainRig(t, 6, 2, 15*time.Minute)
	// g00 g01 asleep, g02 awake, g03 charging, g04 g05 driving
	r.act["v02"] = ActivityAwake
	r.act["v03"], r.kind["v03"] = ActivityBusy, "charge"
	r.act["v04"], r.kind["v04"] = ActivityBusy, "drive"
	r.act["v05"], r.kind["v05"] = ActivityBusy, "drive"
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g00", "g01"}) || !reflect.DeepEqual(r.moves, []string{"idle", "idle"}) {
		t.Fatalf("pass 1 released %v, moves %v, want the two asleep groups", got, r.moves)
	}
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g02", "g03"}) || !reflect.DeepEqual(r.moves, []string{"awake", "charging"}) {
		t.Fatalf("pass 2 released %v, moves %v, want awake then charging", got, r.moves)
	}
	r.now = r.now.Add(10 * time.Minute)
	if got := r.pass(); len(got) != 0 || len(r.moves) != 0 {
		t.Fatalf("pass 3 released %v before the cap with two drives open", got)
	}
	// One drive ends: it goes on the next pass, counted drive_end; the other waits.
	r.act["v04"] = ActivityAwake
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g04"}) || !reflect.DeepEqual(r.moves, []string{"drive_end"}) {
		t.Fatalf("after a drive ended: released %v, moves %v, want g04 as drive_end", got, r.moves)
	}
	if r.held() != 1 {
		t.Fatalf("held %d, want the one driving group", r.held())
	}
}

// At the cap a drive that is still open moves, once, counted cap.
func TestDrain_CapMovesADriveOnce(t *testing.T) {
	r := newDrainRig(t, 2, 4, 15*time.Minute)
	r.act["v00"], r.kind["v00"] = ActivityBusy, "drive"
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g01"}) {
		t.Fatalf("pass 1 released %v, want the asleep group only", got)
	}
	r.now = r.now.Add(14 * time.Minute)
	if got := r.pass(); len(got) != 0 {
		t.Fatalf("released %v at 14 min", got)
	}
	r.now = r.now.Add(2 * time.Minute)
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g00"}) || !reflect.DeepEqual(r.moves, []string{"cap"}) {
		t.Fatalf("at the cap released %v, moves %v, want g00 as cap", got, r.moves)
	}
	if got := r.pass(); len(got) != 0 || r.held() != 0 {
		t.Errorf("a second move: released %v, held %d", got, r.held())
	}
}

// A drive that starts while its group is queued is skipped that pass, not released mid-drive.
func TestDrain_ADriveThatStartsWhileQueuedIsSkipped(t *testing.T) {
	r := newDrainRig(t, 3, 1, 15*time.Minute)
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g00"}) {
		t.Fatalf("pass 1 released %v", got)
	}
	r.act["v01"], r.kind["v01"] = ActivityBusy, "drive" // g01 was next in line
	if got := r.pass(); !reflect.DeepEqual(got, []string{"g02"}) {
		t.Fatalf("pass 2 released %v, want g02 (g01 started a drive)", got)
	}
	if got := r.pass(); len(got) != 0 {
		t.Fatalf("pass 3 released %v with only a drive left", got)
	}
}

// 50 asleep groups hand over in about a minute at the default rate (D1).
func TestDrain_FiftyIdleGroupsInAboutAMinute(t *testing.T) {
	r := newDrainRig(t, 50, DefaultDrainPerPass, 15*time.Minute)
	passes := 0
	for r.held() > 0 && passes < 100 {
		r.pass()
		passes++
	}
	if secs := passes * int(groupTick/time.Second); secs > 60 {
		t.Errorf("50 groups took %d passes (%d s at a 5 s tick), want 60 s or less", passes, secs)
	}
}

// Without a cap (the default) no pass hands a drive over; shutdown does, and counts it forced.
func TestDrain_NoCapKeepsDrivesForShutdown(t *testing.T) {
	r := newDrainRig(t, 2, 4, 0)
	r.act["v00"], r.kind["v00"] = ActivityBusy, "drive"
	r.pass()
	r.now = r.now.Add(10 * time.Hour)
	if got := r.pass(); len(got) != 0 {
		t.Fatalf("released %v with no cap", got)
	}
	r.moves = nil
	r.c.Shutdown(context.Background())
	if !reflect.DeepEqual(r.moves, []string{"forced"}) {
		t.Errorf("shutdown moves %v, want [forced]", r.moves)
	}
}

// DrainWait returns as soon as the pod holds nothing, and not at all (no wait) without a cap.
func TestDrain_WaitReturnsWhenEmpty(t *testing.T) {
	r := newDrainRig(t, 3, 8, 15*time.Minute)
	done := make(chan struct{})
	go func() { r.c.DrainWait(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DrainWait did not return with every group asleep")
	}
	if r.held() != 0 {
		t.Errorf("held %d after DrainWait", r.held())
	}
	r2 := newDrainRig(t, 3, 8, 0)
	r2.c.DrainWait(context.Background()) // returns at once
	if r2.held() != 3 {
		t.Errorf("no cap: held %d, want 3 (nothing handed over in a wait)", r2.held())
	}
}
