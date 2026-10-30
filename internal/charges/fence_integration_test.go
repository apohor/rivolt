//go:build integration

package charges_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/testpg"
)

// The live recorder's charge writes carry the group lease: the upsert and the three UPDATEs of the
// open session are written only while the lease holds, in the same statement.
func TestChargeWrites_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// One pool: testpg.DSN gives each call its own database, so the lease row and the store must
	// share this one.
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "chargefence")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	var epochA int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epochA); err != nil {
		t.Fatal(err)
	}
	tokA := fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epochA}
	gctx := fence.With(ctx, tokA)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	// Parity: the guarded statement stores what the plain one does, including the sticky
	// energy_estimated flag and the network evidence document.
	mk := func(id string, at time.Time) charges.Charge {
		c := sampleCharge(id, "rv-fence", at, "charging_active")
		c.EnergyEstimated = true
		c.NetworkEvidence = json.RawMessage(`{"v":1,"candidates":[{"n":"x","d":12.5}]}`)
		c.Network, c.NetworkState = "ea", "estimated"
		return c
	}
	if err := store.Upsert(ctx, mk("c-plain", start)); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(gctx, mk("c-guard", start.Add(2*time.Hour))); err != nil {
		t.Fatalf("guarded upsert with a valid lease: %v", err)
	}
	var diff int
	if err := pool.QueryRowContext(ctx, `
		SELECT count(*) FROM (
		  SELECT start_soc_pct, end_soc_pct, energy_added_kwh, miles_added, max_power_kw, avg_power_kw, final_state, lat, lon, source,
		         cost, currency, price_per_kwh, thermal_kwh, cost_source, network, network_state, network_evidence, metadata, place
		    FROM charges WHERE external_id = 'c-plain'
		  EXCEPT
		  SELECT start_soc_pct, end_soc_pct, energy_added_kwh, miles_added, max_power_kw, avg_power_kw, final_state, lat, lon, source,
		         cost, currency, price_per_kwh, thermal_kwh, cost_source, network, network_state, network_evidence, metadata, place
		    FROM charges WHERE external_id = 'c-guard') d`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if diff != 0 {
		t.Fatalf("the guarded write stored something different from the plain one (%d differing rows)", diff)
	}
	var est bool
	if err := pool.QueryRowContext(ctx, `SELECT coalesce((metadata->>'energy_estimated')::boolean, false) FROM charges WHERE external_id = 'c-guard'`).Scan(&est); err != nil || !est {
		t.Errorf("energy_estimated not stored by the guarded write: %v %v", est, err)
	}

	finalState := func(id string) string {
		var s string
		if err := pool.QueryRowContext(ctx, `SELECT final_state FROM charges WHERE external_id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The owner may refresh and abandon its open session, and sweep the vehicle's stale ones.
	if n, err := store.RefreshOpenLive(gctx, "c-guard", ""); err != nil || n != 1 {
		t.Fatalf("owner refresh = %d, %v", n, err)
	}
	if n, err := store.CloseStaleOpenLive(gctx, "rv-fence", "c-guard"); err != nil || n != 1 {
		t.Fatalf("owner stale close = %d, %v (want the plain row closed)", n, err)
	}
	if finalState("c-plain") != "abandoned" {
		t.Fatalf("c-plain = %s", finalState("c-plain"))
	}

	// A row that is not there is not a refusal: zero rows, no error, lease valid.
	if n, err := store.AbandonOpenLive(gctx, "nope"); err != nil || n != 0 {
		t.Errorf("abandon of a missing row = %d, %v, want 0, nil", n, err)
	}

	// A steal: the old owner's writes are refused and change nothing; the new owner's are kept.
	var epochB int64
	if err := pool.QueryRowContext(ctx, `UPDATE account_leases SET pod_id = 'pod-b', epoch = nextval('account_lease_epoch'), expires_at = now() + interval '30 seconds' WHERE group_id = $1 RETURNING epoch`, uid).Scan(&epochB); err != nil {
		t.Fatal(err)
	}
	closed := mk("c-guard", start.Add(2*time.Hour))
	closed.FinalState = "charging_complete"
	if err := store.Upsert(gctx, closed); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner's close: err = %v, want ErrFenced", err)
	}
	if _, err := store.AbandonOpenLive(gctx, "c-guard"); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner's abandon: err = %v, want ErrFenced", err)
	}
	if _, err := store.RefreshOpenLive(gctx, "c-guard", "charging_complete"); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner's refresh: err = %v, want ErrFenced", err)
	}
	if _, err := store.CloseStaleOpenLive(gctx, "rv-fence", "x"); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner's stale close: err = %v, want ErrFenced", err)
	}
	if _, err := store.CloseStaleOpenLiveBefore(gctx, time.Now().Add(time.Hour)); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner's stale-before close: err = %v, want ErrFenced", err)
	}
	if got := finalState("c-guard"); got != "charging_active" {
		t.Fatalf("a refused write changed the charge: %s", got)
	}
	nctx := fence.With(ctx, fence.Token{Group: tokA.Group, Pod: "pod-b", Epoch: epochB})
	cont := mk("c-guard", start.Add(2*time.Hour))
	cont.EndSoCPct = 85
	if err := store.Upsert(nctx, cont); err != nil {
		t.Errorf("new owner's continuation: %v", err)
	}
	if err := store.Upsert(nctx, closed); err != nil {
		t.Errorf("new owner's close: %v", err)
	}
	if got := finalState("c-guard"); got != "charging_complete" {
		t.Errorf("final state %s, want the new owner's close", got)
	}
}
