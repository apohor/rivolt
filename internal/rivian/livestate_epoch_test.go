package rivian

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/fence"
)

// The live-state writes are an epoch CAS: a pod whose lease epoch is below the highest that wrote is
// refused, so a paused or partitioned old owner cannot overwrite the new owner's snapshot.
func TestRedisLiveStateEpochCAS(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	s := NewRedisLiveStateStore(rdb, "user-1", "rivolt")
	ctx := context.Background()
	at := func(e int64) context.Context { return fence.With(ctx, fence.Token{Group: "g", Pod: "p", Epoch: e}) }
	snap := func(n int64) LiveStateSnapshot { return LiveStateSnapshot{DriveCounter: n} }
	loaded := func() int64 {
		got, ok, err := s.Load(ctx, "veh")
		if err != nil || !ok {
			t.Fatalf("load = %v %v", ok, err)
		}
		return got.DriveCounter
	}

	// The owner at epoch 5 writes; a newer owner (6) overwrites.
	if err := s.Save(at(5), "veh", snap(1), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(at(5), "veh", snap(2), time.Hour); err != nil { // same epoch: its own repeated write
		t.Fatalf("same-epoch write: %v", err)
	}
	if err := s.Save(at(6), "veh", snap(3), time.Hour); err != nil {
		t.Fatal(err)
	}
	// The old owner wakes: refused, and the new owner's snapshot stands.
	if err := s.Save(at(5), "veh", snap(99), time.Hour); !errors.Is(err, fence.ErrFenced) {
		t.Fatalf("old owner's save: %v, want ErrFenced", err)
	}
	if got := loaded(); got != 3 {
		t.Fatalf("snapshot = %d, want 3 (the new owner's)", got)
	}
	st := &State{VehicleID: "veh", BatteryLevelPct: 61}
	if err := s.PutState(at(6), "veh", st, time.Minute); err != nil {
		t.Fatal(err)
	}
	old := &State{VehicleID: "veh", BatteryLevelPct: 1}
	if err := s.PutState(at(5), "veh", old, time.Minute); !errors.Is(err, fence.ErrFenced) {
		t.Fatalf("old owner's PutState: %v, want ErrFenced", err)
	}
	if got, _ := s.GetState(ctx, "veh"); got == nil || got.BatteryLevelPct != 61 {
		t.Fatalf("published state = %+v, want the new owner's", got)
	}
	// A stale delete does not remove the new owner's snapshot either.
	if err := s.Delete(at(5), "veh"); !errors.Is(err, fence.ErrFenced) {
		t.Fatalf("old owner's delete: %v, want ErrFenced", err)
	}
	if got := loaded(); got != 3 {
		t.Fatalf("snapshot = %d after a refused delete", got)
	}
	if err := s.Delete(at(6), "veh"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Load(ctx, "veh"); ok {
		t.Fatal("the owner's delete did not delete")
	}

	// The epoch outlives the data: after the snapshot expires the old owner is still refused.
	if err := s.Save(at(6), "veh", snap(4), time.Minute); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Minute)
	if _, ok, _ := s.Load(ctx, "veh"); ok {
		t.Fatal("snapshot should have expired")
	}
	if err := s.Save(at(5), "veh", snap(98), time.Hour); !errors.Is(err, fence.ErrFenced) {
		t.Fatalf("old owner after the snapshot expired: %v, want ErrFenced", err)
	}

	// No token (lead off, imports): the plain SET, as before.
	if err := s.Save(ctx, "other", snap(7), time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Load(ctx, "other"); !ok || got.DriveCounter != 7 {
		t.Fatalf("unguarded save: %v %v", got, ok)
	}
}

// The recorder writes its snapshot with the token while it holds the group or is warming up (the
// epoch CAS orders it), and not at all after the group is lost.
func TestRedisCtxByFenceState(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 3}
	for _, c := range []struct {
		name    string
		src     fence.Source
		write   bool
		wantTok bool
		counted int
	}{
		{"off", nil, true, false, 0},
		{"held", fakeFence{tok: tok, st: fence.Held}, true, true, 0},
		{"warming", fakeFence{tok: tok, st: fence.Warming}, true, true, 0},
		{"lost", fakeFence{st: fence.Lost}, false, false, 1},
	} {
		m := NewStateMonitor(nil, nil)
		n := 0
		m.SetFence(c.src, func(k string) {
			if k != "redis" {
				t.Errorf("%s: counted as %q", c.name, k)
			}
			n++
		})
		ctx, ok := m.redisCtx(context.Background(), "veh")
		if ok != c.write {
			t.Errorf("%s: write = %v", c.name, ok)
		}
		if _, has := fence.From(ctx); has != c.wantTok {
			t.Errorf("%s: token = %v", c.name, has)
		}
		if n != c.counted {
			t.Errorf("%s: counted %d, want %d", c.name, n, c.counted)
		}
	}
}

// Account deletion removes the epoch key with the snapshots: it sits under the user's livestate prefix.
func TestPurgeRemovesTheEpochKeyToo(t *testing.T) {
	mr, rdb, p := purgeFixture(t)
	const gone = "11111111-1111-1111-1111-111111111111"
	s := NewRedisLiveStateStore(rdb, gone, "rivolt")
	ctx := fence.With(context.Background(), fence.Token{Group: "g", Pod: "p", Epoch: 4})
	if err := s.Save(ctx, "veh", LiveStateSnapshot{DriveCounter: 1}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(mr.Keys()) != 2 { // the snapshot and its epoch
		t.Fatalf("keys = %v", mr.Keys())
	}
	if _, err := p.PurgeNow(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	if left := mr.Keys(); len(left) != 0 {
		t.Fatalf("keys left after the purge: %v", left)
	}
}
