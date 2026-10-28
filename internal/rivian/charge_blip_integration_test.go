//go:build integration

package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

// The recorder's write guard, against the real charges store: the wake blip (a stale
// "charging_active" frame and its correction 1.6 s later) is not persisted, a short charge that
// moved the battery is, a plugged-in session that outlasts the bound is, and a session that starts
// as a blip and then runs is persisted from the write that crosses the bound.
func TestWakeBlipIsNotPersistedAsACharge(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "charge-blip")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	m := NewStateMonitor(nil, nil)
	m.SetStores(nil, nil, store)

	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	count := func() map[string]bool {
		ids := map[string]bool{}
		if err := store.EachAll(ctx, func(c charges.Charge) error { ids[c.ID] = true; return nil }); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	mk := func(id string, startOff, dur time.Duration, soc0, soc1 float64, state string) *liveCharge {
		return &liveCharge{id: id, startedAt: t0.Add(startOff), endAt: t0.Add(startOff + dur), startSoC: soc0, endSoC: soc1,
			finalState: state, final: true, lastMeaningfulAt: t0.Add(startOff + dur)}
	}

	blip := mk("live_v-blip_c_1", 0, 1600*time.Millisecond, 80.099998, 80.0999984741211, "charging_complete")
	m.upsertLiveCharge(ctx, "v-blip", blip)
	if got := count(); len(got) != 0 {
		t.Fatalf("a 1.6 s flat session was persisted: %v", got)
	}

	short := mk("live_v-blip_c_2", time.Hour, 8*time.Second, 69.4, 70, "charging_complete")
	short.maxPower, short.energyIntKWh = 7.5, 0.017
	m.upsertLiveCharge(ctx, "v-blip", short)
	if got := count(); !got["live_v-blip_c_2"] {
		t.Fatalf("a short charge that moved the battery 0.6 points was not persisted: %v", got)
	}

	plugged := mk("live_v-blip_c_3", 90*time.Minute, 30*time.Second, 65, 65, "charging_complete")
	m.upsertLiveCharge(ctx, "v-blip", plugged)
	if got := count(); !got["live_v-blip_c_3"] {
		t.Fatalf("a 30 s plugged-in session was not persisted: %v", got)
	}

	// Opens as a blip (open writes are skipped), then runs: persisted from the write that crosses the bound.
	grow := mk("live_v-blip_c_4", 100*time.Minute, 5*time.Second, 50, 50, "charging_active")
	grow.final = false
	m.upsertLiveCharge(ctx, "v-blip", grow)
	if got := count(); got["live_v-blip_c_4"] {
		t.Fatalf("a 5 s flat open session was persisted: %v", got)
	}
	grow.endAt, grow.endSoC = t0.Add(100*time.Minute+25*time.Second), 50.2
	m.upsertLiveCharge(ctx, "v-blip", grow)
	if got := count(); !got["live_v-blip_c_4"] {
		t.Fatalf("a session that ran past the bound was never persisted: %v", got)
	}
}
