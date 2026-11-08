//go:build integration

package rivian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
)

type mapFenceSrc map[string]fence.State

func (m mapFenceSrc) Fence(id string) (fence.Token, fence.State) {
	st, ok := m[id]
	if !ok {
		return fence.Token{}, fence.Lost
	}
	return fence.Token{Group: "g-" + id, Pod: "pod-a", Epoch: 3}, st
}

// The stale-charge janitor lists every stale open row on every pod and acts only for the groups the pod holds. A pod
// that does not hold the group skips the row without a count: nothing was attempted, so
// rivolt_lease_fenced_total{kind="charges"} must stay at 0 (it fired once per sweep that saw another pod's stuck row,
// the 70-minute pattern of #1619). The row is left exactly as it was.
func TestJanitorSkipIsNotAFence(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "janitor-fence")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	stale := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	if err := store.Upsert(ctx, charges.Charge{ID: "live_v-jf_c_1", VehicleID: "v-jf", StartedAt: stale.Add(-time.Hour), EndedAt: stale,
		StartSoCPct: 40, EndSoCPct: 41, FinalState: "charging_active", Source: "live"}); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		var s string
		if err := store.EachAll(ctx, func(c charges.Charge) error {
			s = c.FinalState + "|" + c.EndedAt.UTC().Format(time.RFC3339)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := state()

	for name, st := range map[string]fence.State{"lost": fence.Lost, "warming": fence.Warming} {
		var fenced atomic.Int64
		m := NewStateMonitor(NewLive(), nil)
		m.SetStores(nil, nil, store)
		m.SetFence(mapFenceSrc{"v-jf": st}, func(string) { fenced.Add(1) })
		// The janitor's REST confirm is a Rivian call: a pod that does not hold the group must make none (single-owner M3-0, DA).
		var rest atomic.Int64
		m.restFn = func(context.Context, string) (*State, error) {
			rest.Add(1)
			return &State{ChargerState: "charging_active"}, nil
		}
		m.sweepStaleCharges(ctx, time.Hour)
		if n := rest.Load(); n != 0 {
			t.Errorf("%s: a pod that does not hold the group made %d Rivian REST call(s) from the janitor; want 0", name, n)
		}
		if n := fenced.Load(); n != 0 {
			t.Errorf("%s: the sweep counted %d fences for a group it does not hold; want 0 (nothing was attempted)", name, n)
		}
		if got := state(); got != before {
			t.Errorf("%s: a pod that does not hold the group changed the row: %s -> %s", name, before, got)
		}
	}
}
