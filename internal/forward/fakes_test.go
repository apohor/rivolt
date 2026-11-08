package forward_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/forward/forwardtest"
)

// The fakes satisfy the injected interfaces and behave as the later tests will rely on.
func TestFakes(t *testing.T) {
	lv := &forwardtest.LeaseView{Owners: map[string]forward.LeaseOwner{"g": {Found: true, Pod: "p1", Epoch: 4}}, HeldAt: map[string]int64{"h": 9}}
	if o, err := lv.Owner(context.Background(), "g"); err != nil || o.Pod != "p1" || o.Epoch != 4 {
		t.Fatalf("owner: %+v %v", o, err)
	}
	if o, _ := lv.Owner(context.Background(), "none"); o.Found {
		t.Error("an unknown group has no owner")
	}
	if e, ok := lv.Held("h"); !ok || e != 9 {
		t.Error("held")
	}
	if _, ok := lv.Held("g"); ok {
		t.Error("not held")
	}
	rn := &forwardtest.Runner{}
	res, err := rn.Run(context.Background(), forward.KindAdopt, "u", "g", nil)
	if err != nil || res.Status != 200 || len(rn.Calls()) != 1 || rn.Calls()[0].Kind != forward.KindAdopt {
		t.Errorf("runner: %+v %v %v", res, err, rn.Calls())
	}
	ck := forwardtest.NewClock(1000)
	ck.Advance(5 * time.Second)
	if ms, _ := ck.RedisTimeMS(context.Background()); ms != 6000 {
		t.Errorf("clock = %d", ms)
	}
}
