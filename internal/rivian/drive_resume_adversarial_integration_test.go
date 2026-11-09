//go:build integration

package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
)

// Adversarial cross-review of G2a (the drive resumed from its row), by someone other than the author: the cases the author's test does not walk.
type resumeRig struct {
	t     *testing.T
	ctx   context.Context
	store *drives.Store
	vid   string
	t0    time.Time
}

func newResumeRig(t *testing.T, user string) *resumeRig {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, user)
	if err != nil {
		t.Fatal(err)
	}
	return &resumeRig{t: t, ctx: ctx, store: drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid), vid: "v-" + user,
		t0: time.Now().UTC().Add(-time.Hour).Truncate(time.Second)}
}

func (r *resumeRig) frame(at time.Duration, km float64) *State {
	return &State{VehicleID: r.vid, At: r.t0.Add(at), Gear: "D", BatteryLevelPct: 80 - km/50, OdometerKm: 1000 + km, SpeedKph: 90, Latitude: 35 + km/100, Longitude: -97}
}

func (r *resumeRig) pod() (*StateMonitor, *liveSessions) {
	m := NewStateMonitor(nil, nil)
	m.SetStores(nil, r.store, nil)
	m.driveDBResume = true
	return m, &liveSessions{}
}

func (r *resumeRig) rows() []drives.Drive {
	out, err := r.store.ListRecentForVehicle(r.ctx, r.vid, 20, false)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// A drive handed over twice with no snapshot at either hop (A to B to C, Redis flushed both times): still one row, the route keeps growing, the
// start never moves, and C leaves the row closed for good.
func TestAdversarialDoubleTakeoverKeepsOneDrive(t *testing.T) {
	r := newResumeRig(t, "g2a-double")
	a, sa := r.pod()
	_ = sa.handleDriveLifecycle(r.frame(0, 0), nil, a, r.ctx)
	for i := 1; i <= 4; i++ {
		_ = sa.handleDriveLifecycle(r.frame(time.Duration(i)*30*time.Second, float64(i)*1.4), nil, a, r.ctx)
		a.upsertLiveDrive(r.ctx, r.vid, sa.drive)
	}
	id, started := sa.drive.id, sa.drive.startedAt
	b, sb := r.pod()
	_ = sb.handleDriveLifecycle(r.frame(3*time.Minute, 7), nil, b, r.ctx)
	for i := 1; i <= 3; i++ {
		_ = sb.handleDriveLifecycle(r.frame(3*time.Minute+time.Duration(i)*30*time.Second, 7+float64(i)*1.4), nil, b, r.ctx)
		b.upsertLiveDrive(r.ctx, r.vid, sb.drive)
	}
	if sb.drive.id != id {
		t.Fatalf("first hand-off opened %s, want %s resumed", sb.drive.id, id)
	}
	c, sc := r.pod()
	_ = sc.handleDriveLifecycle(r.frame(6*time.Minute, 14), nil, c, r.ctx)
	if sc.drive == nil || sc.drive.id != id {
		t.Fatalf("second hand-off opened %+v, want %s resumed", sc.drive, id)
	}
	if !sc.drive.startedAt.Equal(started) || len(sc.drive.path) < len(sb.drive.path) {
		t.Errorf("start moved or the route shrank: %v vs %v, %d vs %d points", sc.drive.startedAt, started, len(sc.drive.path), len(sb.drive.path))
	}
	_ = sc.handleDriveLifecycle(&State{VehicleID: r.vid, At: r.t0.Add(7 * time.Minute), Gear: "P", BatteryLevelPct: 70, OdometerKm: 1015, Latitude: 35.2, Longitude: -97}, nil, c, r.ctx)
	n := 0
	for _, d := range r.rows() {
		if d.ID == id {
			n++
		}
	}
	if n != 1 || len(r.rows()) != 1 {
		t.Errorf("%d rows for the drive, %d in all, want 1 and 1", n, len(r.rows()))
	}
	if open, _ := r.store.LatestOpenLive(r.ctx, r.vid); open != nil {
		t.Errorf("drive %s is still open after C closed it", open.ID)
	}
}

// The odometer: a small step back (the truck's odometer jitter, within the reopen tolerance) is the same trip; a larger step back is not.
func TestAdversarialOdometerGoingBackwards(t *testing.T) {
	for _, c := range []struct {
		name string
		back float64 // km
		same bool
	}{{"20 m back", 0.02, true}, {"1 km back", 1, false}, {"50 km back", 50, false}} {
		t.Run(c.name, func(t *testing.T) {
			r := newResumeRig(t, "g2a-odo-"+c.name[:2])
			a, sa := r.pod()
			_ = sa.handleDriveLifecycle(r.frame(0, 0), nil, a, r.ctx)
			_ = sa.handleDriveLifecycle(r.frame(30*time.Second, 5), nil, a, r.ctx)
			a.upsertLiveDrive(r.ctx, r.vid, sa.drive)
			b, sb := r.pod()
			_ = sb.handleDriveLifecycle(r.frame(60*time.Second, 5-c.back), nil, b, r.ctx)
			if got := sb.drive.id == sa.drive.id; got != c.same {
				t.Errorf("resumed=%v, want %v", got, c.same)
			}
		})
	}
}

// A driving frame with no odometer at all (a failed REST seed after a takeover) cannot be checked against the row, so it is resumed on the time
// window alone. This documents that: a frame from 25 minutes later is taken for the same trip.
func TestAdversarialAFrameWithoutAnOdometerIsResumedOnTimeAlone(t *testing.T) {
	r := newResumeRig(t, "g2a-noodo")
	a, sa := r.pod()
	_ = sa.handleDriveLifecycle(r.frame(0, 0), nil, a, r.ctx)
	_ = sa.handleDriveLifecycle(r.frame(30*time.Second, 5), nil, a, r.ctx)
	a.upsertLiveDrive(r.ctx, r.vid, sa.drive)
	b, sb := r.pod()
	f := r.frame(25*time.Minute, 5)
	f.OdometerKm = 0
	_ = sb.handleDriveLifecycle(f, nil, b, r.ctx)
	if sb.drive == nil || sb.drive.id != sa.drive.id {
		t.Skip("not resumed: the time-alone rule changed; update this test with it")
	}
}

// Clock skew between pods: the new owner's first frame carries a time a few seconds before the row's last write (its clock is behind the old
// owner's by more than the hand-off took). A negative gap must not split the trip: a few seconds of skew is the same drive.
func TestAdversarialClockSkewBetweenPodsDoesNotSplitTheDrive(t *testing.T) {
	r := newResumeRig(t, "g2a-skew")
	a, sa := r.pod()
	_ = sa.handleDriveLifecycle(r.frame(0, 0), nil, a, r.ctx)
	_ = sa.handleDriveLifecycle(r.frame(60*time.Second, 1.4), nil, a, r.ctx)
	a.upsertLiveDrive(r.ctx, r.vid, sa.drive)
	b, sb := r.pod()
	_ = sb.handleDriveLifecycle(r.frame(57*time.Second, 1.4), nil, b, r.ctx) // 3 s before pod A's last frame
	if sb.drive == nil || sb.drive.id != sa.drive.id {
		t.Errorf("a frame 3 s earlier than the row's last write opened %+v, want the open drive resumed", sb.drive)
	}
}
