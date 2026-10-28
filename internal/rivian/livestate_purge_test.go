package rivian

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func purgeFixture(t *testing.T) (*miniredis.Miniredis, *redis.Client, *LiveStatePurger) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb, &LiveStatePurger{Rdb: rdb, Prefix: "rivolt", Again: 40 * time.Millisecond}
}

func TestPurgeRemovesOnlyTheDeletedUsersLiveStateKeys(t *testing.T) {
	mr, rdb, p := purgeFixture(t)
	ctx := context.Background()
	const gone, other = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	set := func(k string) {
		if err := rdb.Set(ctx, k, "x", time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// More keys than one SCAN batch, so the cursor loop is exercised.
	for i := 0; i < 450; i++ {
		set(fmt.Sprintf("rivolt:livestate:%s:veh%d", gone, i))
	}
	set("rivolt:livestate:" + gone + ":veh1:state")
	set("rivolt:livestate:" + other + ":veh1")
	set("rivolt:livestate:" + other + ":veh1:state")
	set("rivolt-preview:livestate:" + gone + ":veh1") // another install on the same Redis
	set("rivolt:planquota:" + gone + ":2026102804")   // a count, not location data; not this purge's business
	set("rivolt:fail:login:abc")

	n, err := p.PurgeNow(ctx, gone)
	if err != nil {
		t.Fatal(err)
	}
	if n != 451 {
		t.Fatalf("removed %d keys, want 451", n)
	}
	left := mr.Keys()
	if len(left) != 5 {
		t.Fatalf("keys left = %v, want the other user's 2, the other install's, the quota and the limiter", left)
	}
	for _, k := range left {
		if k == "rivolt:livestate:"+gone+":veh0" {
			t.Fatal("a deleted user's key survived")
		}
	}
}

// A monitor on another pod can write once more before it notices the deletion and stops; the
// second pass removes that write, so nothing of the user outlives the account.
func TestPurgeRunsAgainForAWriteThatWasInFlight(t *testing.T) {
	mr, rdb, p := purgeFixture(t)
	ctx := context.Background()
	const gone = "11111111-1111-1111-1111-111111111111"
	key := "rivolt:livestate:" + gone + ":veh1"
	if err := rdb.Set(ctx, key, "snapshot", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	p.Purge(ctx, gone) // first pass now
	if mr.Exists(key) {
		t.Fatal("the key survived the first pass")
	}
	// The peer's monitor writes again, renewing the two-hour TTL.
	if err := rdb.Set(ctx, key, "snapshot-again", 2*time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for mr.Exists(key) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mr.Exists(key) {
		t.Fatal("the late write outlived the second pass")
	}
}

func TestPurgeNeverFailsTheDeletionAndNeedsNoRedis(t *testing.T) {
	mr, _, p := purgeFixture(t)
	mr.Close()
	if _, err := p.PurgeNow(context.Background(), "11111111-1111-1111-1111-111111111111"); err == nil {
		t.Fatal("expected an error to be reported for logging")
	}
	p.Purge(context.Background(), "11111111-1111-1111-1111-111111111111") // logs, does not panic or block
	var none *LiveStatePurger
	none.Purge(context.Background(), "x")
	if n, err := none.PurgeNow(context.Background(), "x"); n != 0 || err != nil {
		t.Fatalf("a nil purger: %d %v", n, err)
	}
	if n, err := (&LiveStatePurger{}).PurgeNow(context.Background(), "x"); n != 0 || err != nil {
		t.Fatalf("no Redis: %d %v", n, err)
	}
}

// Real Redis keys are written by RedisLiveStateStore; the purge covers exactly its layout.
func TestPurgeCoversWhatTheLiveStateStoreWrites(t *testing.T) {
	mr, rdb, p := purgeFixture(t)
	ctx := context.Background()
	const uid = "11111111-1111-1111-1111-111111111111"
	st := NewRedisLiveStateStore(rdb, uid, "rivolt")
	if err := st.Save(ctx, "veh1", LiveStateSnapshot{}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.PutState(ctx, "veh1", &State{}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(mr.Keys()) != 2 {
		t.Fatalf("setup: keys = %v", mr.Keys())
	}
	if n, err := p.PurgeNow(ctx, uid); err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want both the snapshot and the published state", n, err)
	}
	if len(mr.Keys()) != 0 {
		t.Fatalf("keys left: %v", mr.Keys())
	}
}
