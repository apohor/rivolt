//go:build integration

package receipts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// #1885: a matched receipt's energy goes onto a closed charge, survives the recorder saving the row again,
// leaves a user's own price alone, and an opt-out purge puts back what we recorded.
func TestReceiptEnergySurvivesResaveAndPurgeRestores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "receiptenergy")
	if err != nil {
		t.Fatal(err)
	}
	cs := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	st := receipts.NewStore(pool)

	end := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	mk := func(id, state string, cost float64, src string) charges.Charge {
		e := end
		if id == "re-user" {
			e = end.Add(-24 * time.Hour)
		}
		return charges.Charge{
			ID: id, VehicleID: "re-rv", StartedAt: e.Add(-17 * time.Minute), EndedAt: e,
			StartSoCPct: 20, EndSoCPct: 60, EnergyAddedKWh: 26.80, MilesAdded: 100, MaxPowerKW: 150, AvgPowerKW: 94,
			FinalState: state, Lat: 30.27, Lon: -97.74, Source: "live",
			Cost: cost, Currency: "USD", PricePerKWh: 0.40, CostSource: src,
		}
	}
	for _, c := range []charges.Charge{
		mk("re-dc", "charging_complete", 10.72, "observed_estimate"),
		mk("re-user", "charging_complete", 9.99, "user"), // stands in for another session; made open below
	} {
		if err := cs.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	kwh := 27.42
	cost := 11.0
	sums := []rivian.SessionSummary{{TransactionID: "tx-dc", StartedAt: end.Add(-17 * time.Minute), EndedAt: end.Add(41 * time.Second),
		TotalCost: &cost, Currency: "USD", EnergyKWh: &kwh}}
	var chargeID, otherID string
	if err := pool.QueryRowContext(ctx, `SELECT id::text FROM charges WHERE user_id=$1 AND external_id='re-dc'`, uid).Scan(&chargeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT id::text FROM charges WHERE user_id=$1 AND external_id='re-user'`, uid).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	var cands []receipts.EnergyCand
	var up *receipts.EnergyUpdate
	link := func() {
		t.Helper()
		if err := st.UpsertReceipts(ctx, uid, sums); err != nil {
			t.Fatal(err)
		}
		rs, err := st.Unmatched(ctx, uid)
		if err != nil || len(rs) != 1 {
			t.Fatalf("unmatched %v %v", rs, err)
		}
		if err := st.Apply(ctx, uid, []receipts.Pair{{ReceiptID: rs[0].ID, ChargeID: uuidOf(t, chargeID)}}); err != nil {
			t.Fatal(err)
		}
		home := false
		if err := st.SaveDetail(ctx, uid, "tx-dc", &rivian.SessionCharger{Name: "Rivian Adventure Network", IsHome: &home}); err != nil {
			t.Fatal(err)
		}

		cands, err = st.EnergyCandidates(ctx, uid, time.Now().Add(-time.Hour))
		if err != nil || len(cands) != 1 {
			t.Fatalf("candidates %v %v", cands, err)
		}
		var outcome string
		outcome, _, up = receipts.EvaluateEnergy(cands[0])
		if outcome != receipts.OutcomeWouldApply || up == nil {
			t.Fatalf("evaluate: %q %v", outcome, up)
		}
		ok, aerr := st.ApplyEnergy(ctx, uid, cands[0], *up)
		if aerr != nil || !ok {
			t.Fatalf("apply: %v %v", ok, aerr)
		}

	}
	link()
	// A second apply with the stale expectation changes nothing.
	if ok, _ := st.ApplyEnergy(ctx, uid, cands[0], *up); ok {
		t.Error("stale apply wrote again")
	}

	get := func(ext string) *charges.Charge {
		all, err := cs.ListAll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range all {
			if all[i].ID == ext {
				return &all[i]
			}
		}
		t.Fatalf("charge %s not listed", ext)
		return nil
	}
	got := get("re-dc")
	if got.EnergyAddedKWh != 27.42 || got.EnergySource != "rivian_receipt" || got.EnergyRecordedKWh != 26.80 {
		t.Fatalf("after apply: kwh %v source %q recorded %v", got.EnergyAddedKWh, got.EnergySource, got.EnergyRecordedKWh)
	}
	if want := 0.40 * 27.42; got.Cost < want-0.001 || got.Cost > want+0.001 {
		t.Errorf("cost %v, want price x receipt energy %v", got.Cost, want)
	}
	if got.ReceiptCost == nil || *got.ReceiptCost != 11.0 {
		t.Errorf("receipt cost %v", got.ReceiptCost)
	}

	// The recorder saves the row again (late lifecycle upsert): receipt energy, power and cost stay.
	if err := cs.Upsert(ctx, mk("re-dc", "charging_complete", 10.72, "observed_estimate")); err != nil {
		t.Fatal(err)
	}
	got = get("re-dc")
	if got.EnergyAddedKWh != 27.42 || got.AvgPowerKW == 94 || got.EnergySource != "rivian_receipt" || got.EnergyRecordedKWh != 26.80 {
		t.Errorf("after re-save: kwh %v avg %v source %q recorded %v", got.EnergyAddedKWh, got.AvgPowerKW, got.EnergySource, got.EnergyRecordedKWh)
	}
	if want := 0.40 * 27.42; got.Cost < want-0.001 || got.Cost > want+0.001 {
		t.Errorf("cost after re-save %v, want %v", got.Cost, want)
	}

	// A re-save with a new estimate (other price) keeps the receipt energy and prices it.
	c2 := mk("re-dc", "charging_complete", 12.5, "observed_estimate")
	c2.PricePerKWh = 0.50
	if err := cs.Upsert(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if got = get("re-dc"); got.EnergyAddedKWh != 27.42 || got.Cost < 0.50*27.42-0.001 || got.Cost > 0.50*27.42+0.001 || got.PricePerKWh != 0.50 {
		t.Errorf("new estimate: kwh %v cost %v ppk %v, want 27.42 and 0.50 x 27.42", got.EnergyAddedKWh, got.Cost, got.PricePerKWh)
	}
	// The row open again (the recorder reopening it) takes the recorder's figures.
	c3 := mk("re-dc", "charging_active", 10.72, "observed_estimate")
	if err := cs.Upsert(ctx, c3); err != nil {
		t.Fatal(err)
	}
	if got = get("re-dc"); got.EnergyAddedKWh != 26.80 {
		t.Errorf("reopened row kept receipt energy %v", got.EnergyAddedKWh)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET final_state='charging_complete', energy_added_kwh=27.42, cost=10.968, price_per_kwh=0.40 WHERE id=$1`, uuidOf(t, chargeID)); err != nil {
		t.Fatal(err)
	}

	// An open charge is refused by the write itself, even when a stale candidate says otherwise.
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET final_state='charging_active', energy_added_kwh=26.80, receipt_txn_id='tx-dc' WHERE id=$1`, uuidOf(t, otherID)); err != nil {
		t.Fatal(err)
	}
	stale := cands[0]
	stale.ChargeID, stale.Energy = uuidOf(t, otherID), 26.80
	if ok, _ := st.ApplyEnergy(ctx, uid, stale, *up); ok {
		t.Error("wrote to an open charge")
	}

	// Opt-out purge restores what the recorder saved.
	if err := st.Purge(ctx, uid); err != nil {
		t.Fatal(err)
	}
	got = get("re-dc")
	if got.EnergyAddedKWh != 26.80 || got.EnergySource != "" || got.EnergyRecordedKWh != 0 || got.AvgPowerKW != 94 || got.ReceiptCost != nil {
		t.Errorf("after purge: kwh %v source %q recorded %v avg %v receipt %v", got.EnergyAddedKWh, got.EnergySource, got.EnergyRecordedKWh, got.AvgPowerKW, got.ReceiptCost)
	}
	if got.Cost < 10.71 || got.Cost > 10.73 {
		t.Errorf("cost after purge %v, want 10.72", got.Cost)
	}

	// The user prices the charge after the receipt's energy went in: a purge must not put our cost back over it.
	link()
	if _, err := pool.ExecContext(ctx, `UPDATE charges SET cost=5, cost_source='user' WHERE id=$1`, uuidOf(t, chargeID)); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if got = get("re-dc"); got.Cost != 5 || got.CostSource != "user" || got.EnergyAddedKWh != 26.80 {
		t.Errorf("user price after purge: cost %v source %q kwh %v, want 5, user, 26.80", got.Cost, got.CostSource, got.EnergyAddedKWh)
	}
}

func uuidOf(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
