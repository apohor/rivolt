//go:build integration

package rivian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/fence"
)

// The janitor takes the "open" marker off live drive rows nobody will resume (last write over an hour old), leaves a recent open drive alone, and
// acts only for groups the pod holds: a pod that does not hold the group changes nothing and counts no fence.
func TestOpenDriveJanitor(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "drive-janitor")
	if err != nil {
		t.Fatal(err)
	}
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	now := time.Now().UTC().Truncate(time.Second)
	add := func(id string, ended time.Time) {
		if err := store.Upsert(ctx, drives.Drive{ID: id, VehicleID: "v-dj", StartedAt: ended.Add(-10 * time.Minute), EndedAt: ended,
			StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 100, EndOdometerMi: 110, DistanceMi: 10, Source: "live"}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE drives SET metadata = metadata || '{"open": true}'::jsonb WHERE external_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	add("live_v-dj_d_old", now.Add(-3*time.Hour))
	add("live_v-dj_d_new", now.Add(-10*time.Minute))
	open := func() map[string]bool {
		out := map[string]bool{}
		rows, err := pool.QueryContext(ctx, `SELECT external_id FROM drives WHERE user_id = $1 AND metadata->>'open' = 'true'`, uid)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out[id] = true
		}
		return out
	}

	// A pod that does not hold the group: nothing changes, no fence is counted.
	var fenced atomic.Int64
	notOwner := NewStateMonitor(NewLive(), nil)
	notOwner.SetStores(nil, store, nil)
	notOwner.SetFence(mapFenceSrc{"v-dj": fence.Lost}, func(string) { fenced.Add(1) })
	notOwner.sweepStaleOpenDrives(ctx, time.Hour)
	if got := open(); !got["live_v-dj_d_old"] || !got["live_v-dj_d_new"] || fenced.Load() != 0 {
		t.Fatalf("a pod that does not hold the group changed rows or counted fences: open %v fenced %d", got, fenced.Load())
	}

	// The owner (here: no lead mode, unguarded) clears the old one only; the drive row itself stays.
	owner := NewStateMonitor(NewLive(), nil)
	owner.SetStores(nil, store, nil)
	owner.sweepStaleOpenDrives(ctx, time.Hour)
	if got := open(); got["live_v-dj_d_old"] || !got["live_v-dj_d_new"] {
		t.Errorf("after the sweep open = %v, want only the recent drive", got)
	}
	if d, err := store.Get(ctx, "live_v-dj_d_old"); err != nil || d == nil || d.DistanceMi != 10 {
		t.Errorf("the drive row was touched: %+v %v", d, err)
	}
}
