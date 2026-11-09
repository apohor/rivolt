//go:build integration

package rivian

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/fence"
)

// G2a: a takeover without the Redis snapshot continues the drive from its row. Pod A opens and writes a drive; pod B, a new monitor with no
// snapshot to rehydrate from, gets the next driving frame and must carry on the same row (one drive, the route continuing), close it for good
// (no longer open), and never glue the next trip onto it. With the flag off, or when the row is too old or the odometer cannot be the same
// trip, B opens a new drive as before.
func TestDriveResumesFromTheDatabaseAfterATakeover(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "drive-resume")
	if err != nil {
		t.Fatal(err)
	}
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	const vid = "v-dr"

	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	frame := func(at time.Time, gear string, km float64, lat float64) *State {
		return &State{VehicleID: vid, At: at, Gear: gear, BatteryLevelPct: 80 - km/50, OdometerKm: 1000 + km, SpeedKph: 90, Latitude: lat, Longitude: -97}
	}
	mon := func(resume bool) *StateMonitor {
		m := NewStateMonitor(nil, nil)
		m.SetStores(nil, store, nil)
		m.driveDBResume = resume
		return m
	}
	rows := func() []drives.Drive {
		out, err := store.ListRecentForVehicle(ctx, vid, 20, false)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Pod A drives for a few minutes; its last write is the row pod B will find.
	a, sa := mon(true), &liveSessions{}
	_ = sa.handleDriveLifecycle(frame(t0, "D", 0, 35.00), nil, a, ctx)
	for i := 1; i <= 6; i++ {
		_ = sa.handleDriveLifecycle(frame(t0.Add(time.Duration(i)*30*time.Second), "D", float64(i)*1.4, 35.00+float64(i)*0.01), nil, a, ctx)
		a.upsertLiveDrive(ctx, vid, sa.drive) // the periodic touch, made explicit
	}
	first := sa.drive.id
	pathA := len(sa.drive.path)
	if got := rows(); len(got) != 1 || got[0].ID != first {
		t.Fatalf("pod A left %d rows, want its one drive", len(got))
	}

	// Pod B, flag off: the old behaviour, a second drive.
	off, soff := mon(false), &liveSessions{}
	_ = soff.handleDriveLifecycle(frame(t0.Add(4*time.Minute), "D", 9.5, 35.07), nil, off, ctx)
	if soff.drive.id == first {
		t.Fatal("flag off: the drive was resumed")
	}
	// A row too old, or an odometer that cannot be the same trip, is not resumed either (the flag-off drive above is not "open" for long: it is
	// a new drive, so close it out of the way for the checks below).
	soff.drive.final = true
	off.upsertLiveDrive(ctx, vid, soff.drive)

	farOdo, sfar := mon(true), &liveSessions{}
	_ = sfar.handleDriveLifecycle(frame(t0.Add(4*time.Minute), "D", 400, 35.07), nil, farOdo, ctx)
	if sfar.drive.id == first {
		t.Error("an odometer 400 km on in four minutes was resumed")
	}
	sfar.drive.final = true
	farOdo.upsertLiveDrive(ctx, vid, sfar.drive)
	tooOld, sold := mon(true), &liveSessions{}
	_ = sold.handleDriveLifecycle(frame(t0.Add(40*time.Minute), "D", 9.8, 35.07), nil, tooOld, ctx)
	if sold.drive.id == first {
		t.Error("a row last written 40 minutes before was resumed")
	}
	sold.drive.final = true
	tooOld.upsertLiveDrive(ctx, vid, sold.drive)

	// Pod B, flag on, no snapshot: the same drive carries on.
	b, sb := mon(true), &liveSessions{}
	_ = sb.handleDriveLifecycle(frame(t0.Add(4*time.Minute), "D", 9.5, 35.07), nil, b, ctx)
	if sb.drive == nil || sb.drive.id != first {
		t.Fatalf("pod B opened %v, want the open drive %s resumed", sb.drive, first)
	}
	if len(sb.drive.path) < pathA {
		t.Errorf("the route restarted: %d points, pod A had %d", len(sb.drive.path), pathA)
	}
	if sb.drive.startedAt != sa.drive.startedAt || sb.drive.startOdoMi != sa.drive.startOdoMi {
		t.Errorf("start lost: %v/%v vs %v/%v", sb.drive.startedAt, sb.drive.startOdoMi, sa.drive.startedAt, sa.drive.startOdoMi)
	}
	for i := 1; i <= 3; i++ {
		_ = sb.handleDriveLifecycle(frame(t0.Add(4*time.Minute+time.Duration(i)*30*time.Second), "D", 9.5+float64(i)*1.4, 35.07+float64(i)*0.01), nil, b, ctx)
	}
	// It parks: the row closes for good.
	_ = sb.handleDriveLifecycle(&State{VehicleID: vid, At: t0.Add(7 * time.Minute), Gear: "P", BatteryLevelPct: 70, OdometerKm: 1014, Latitude: 35.1, Longitude: -97}, nil, b, ctx)
	if sb.drive != nil {
		t.Fatalf("the drive did not close: %+v", sb.drive)
	}
	open, err := store.LatestOpenLive(ctx, vid)
	if err != nil {
		t.Fatal(err)
	}
	if open != nil {
		t.Errorf("the closed drive %s is still marked open", open.ID)
	}
	n := 0
	for _, d := range rows() {
		if d.ID == first {
			n++
			if d.EndOdometerMi < sa.drive.endOdoMi {
				t.Errorf("the row's end odometer %.1f is behind pod A's %.1f", d.EndOdometerMi, sa.drive.endOdoMi)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d rows for the drive", n)
	}

	// A new trip later starts a new drive: nothing open to glue it onto.
	c, sc := mon(true), &liveSessions{}
	_ = sc.handleDriveLifecycle(frame(t0.Add(20*time.Minute), "D", 15, 35.2), nil, c, ctx)
	if sc.drive == nil || sc.drive.id == first {
		t.Errorf("the next trip reused the closed drive: %+v", sc.drive)
	}
}

// Under the group lease: the row a new owner resumed is written by the new epoch, and the old owner's late write of the same row is refused.
func TestResumedDriveIsWrittenByTheNewEpochAndTheOldOneIsRefused(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "drive-resume-fence")
	if err != nil {
		t.Fatal(err)
	}
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	const vid = "v-drf"
	lease := func(pod string) fence.Token {
		var epoch int64
		if err := pool.QueryRowContext(ctx, `INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
			VALUES ($1, $2, nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
			ON CONFLICT (group_id) DO UPDATE SET pod_id = EXCLUDED.pod_id, epoch = EXCLUDED.epoch, expires_at = EXCLUDED.expires_at, released_at = NULL
			RETURNING epoch`, uid, pod).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		return fence.Token{Group: uid.String(), Pod: pod, Epoch: epoch}
	}
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	row := drives.Drive{ID: "live_v-drf_d_1", VehicleID: vid, StartedAt: t0, EndedAt: t0.Add(2 * time.Minute), StartSoCPct: 80, EndSoCPct: 78,
		StartOdometerMi: 600, EndOdometerMi: 610, DistanceMi: 10, Source: "live", Open: true}

	old := lease("pod-old")
	if err := store.Upsert(fence.With(ctx, old), row); err != nil {
		t.Fatalf("the old owner's write: %v", err)
	}
	cur := lease("pod-new") // the takeover: a new epoch
	found, err := store.LatestOpenLive(ctx, vid)
	if err != nil || found == nil || found.ID != row.ID {
		t.Fatalf("the new owner finds %v (%v), want the open row", found, err)
	}
	// The resumed drive's first write, after the warm-up, lands...
	resumed := *found
	resumed.EndedAt = resumed.EndedAt.Add(3 * time.Minute)
	resumed.EndOdometerMi += 5
	resumed.Open = true
	if err := store.Upsert(fence.With(ctx, cur), resumed); err != nil {
		t.Fatalf("the new epoch's write was refused: %v", err)
	}
	// ...and the old epoch's late write of the same drive is refused and changes nothing.
	late := row
	late.EndedAt = row.EndedAt.Add(30 * time.Second)
	if err := store.Upsert(fence.With(ctx, old), late); err == nil || !errors.Is(err, fence.ErrFenced) {
		t.Fatalf("the old epoch's late write: %v, want fence.ErrFenced", err)
	}
	got, err := store.LatestOpenLive(ctx, vid)
	if err != nil || got == nil || got.EndOdometerMi != resumed.EndOdometerMi || !got.EndedAt.Equal(resumed.EndedAt) {
		t.Errorf("the row after the refused write: %+v (%v), want the new epoch's", got, err)
	}
}
