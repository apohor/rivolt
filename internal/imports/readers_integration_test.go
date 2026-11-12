//go:build integration

package imports_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/imports"
	"github.com/apohor/rivolt/internal/milestones"
	"github.com/apohor/rivolt/internal/packhealth"
	"github.com/apohor/rivolt/internal/plugreminder"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
)

// §4b: only live rows trigger anything. An imported drive or charge must not make a vehicle active for
// milestones, count as a plug-in for the reminder, or feed pack health; and a receipt may price an
// imported charge (R3) but its energy is never written into it.
func TestImportedRowsTriggerNothingInOtherReaders(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-3 * time.Hour)

	id, err := e.is.Begin(ctx, imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("d"), Bytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := charge("imp-c1", start, 40*time.Minute)
	c.MaxPowerKW, c.Lat, c.Lon = 150, 30.27, -97.74
	if ok, err := e.cs.InsertImported(ctx, id, c, nil); err != nil || !ok {
		t.Fatalf("InsertImported charge = %v, %v", ok, err)
	}
	if ok, err := e.ds.InsertImported(ctx, id, drive("imp-d1", start.Add(-2*time.Hour), time.Hour), nil); err != nil || !ok {
		t.Fatalf("InsertImported drive = %v, %v", ok, err)
	}
	var vehicle uuid.UUID
	if err := e.pool.QueryRow(`SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2`, e.uid, vin).Scan(&vehicle); err != nil {
		t.Fatal(err)
	}

	active := func() bool {
		t.Helper()
		vs, err := milestones.NewStore(e.pool).Vehicles(ctx, now.Add(-24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			if v.ID == vehicle {
				return true
			}
		}
		return false
	}
	plugged := func() bool {
		t.Helper()
		src := plugreminder.NewSource(e.pool, nil, uuid.Nil)
		ok, err := src.RecentlyPlugged(ctx, e.uid, 30.27, -97.74, now.Add(-6*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	backfill := func() int {
		t.Helper()
		n, err := packhealth.NewStore(e.pool).BackfillVehicle(ctx, vehicle)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if active() {
		t.Error("an imported drive and charge made the vehicle milestone-active")
	}
	if plugged() {
		t.Error("an imported charge counted as a recent plug-in")
	}
	if n := backfill(); n != 0 {
		t.Errorf("pack health took %d imported charge(s)", n)
	}

	// The same shapes from the recorder do trigger, so the zeros above are the filter, not empty data.
	live := charge("live-c1", start.Add(-5*time.Hour), 40*time.Minute)
	live.Source, live.MaxPowerKW, live.Lat, live.Lon = "live", 150, 30.27, -97.74
	live.StartedAt = now.Add(-2 * time.Hour)
	live.EndedAt = live.StartedAt.Add(40 * time.Minute)
	if err := e.cs.Upsert(ctx, live); err != nil {
		t.Fatal(err)
	}
	if !active() {
		t.Error("a live charge did not make the vehicle active")
	}
	if !plugged() {
		t.Error("a live charge did not count as a plug-in")
	}
	if n := backfill(); n != 1 {
		t.Errorf("pack health took %d live charge(s), want 1", n)
	}
}

// R3, both ways: the receipt's cost lands on the imported charge, its energy does not.
func TestReceiptPricesImportedChargeButNeverWritesItsEnergy(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	end := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	id, err := e.is.Begin(ctx, imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("e"), Bytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := charge("imp-r1", end.Add(-17*time.Minute), 17*time.Minute)
	c.EnergyAddedKWh, c.MaxPowerKW = 26.8, 150
	if ok, err := e.cs.InsertImported(ctx, id, c, map[string]any{"energy_basis": "wall"}); err != nil || !ok {
		t.Fatalf("InsertImported = %v, %v", ok, err)
	}
	kwh, cost := 27.42, 11.0
	st := receipts.NewStore(e.pool)
	if err := st.UpsertReceipts(ctx, e.uid, []rivian.SessionSummary{{TransactionID: "tx-imp", StartedAt: c.StartedAt, EndedAt: end.Add(41 * time.Second),
		TotalCost: &cost, Currency: "USD", EnergyKWh: &kwh}}); err != nil {
		t.Fatal(err)
	}
	rs, err := st.Unmatched(ctx, e.uid)
	if err != nil || len(rs) != 1 {
		t.Fatalf("unmatched %v %v", rs, err)
	}
	var chargeID uuid.UUID
	if err := e.pool.QueryRow(`SELECT id FROM charges WHERE user_id = $1`, e.uid).Scan(&chargeID); err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(ctx, e.uid, []receipts.Pair{{ReceiptID: rs[0].ID, ChargeID: chargeID}}); err != nil {
		t.Fatal(err)
	}
	var rc float64
	if err := e.pool.QueryRow(`SELECT receipt_cost::float8 FROM charges WHERE id = $1`, chargeID).Scan(&rc); err != nil || rc != cost {
		t.Fatalf("receipt_cost = %v, %v; the billed figure should reach an imported charge", rc, err)
	}
	cands, err := st.EnergyCandidates(ctx, e.uid, time.Now().Add(-24*time.Hour))
	if err != nil || len(cands) != 0 {
		t.Fatalf("energy candidates = %v, %v; an imported charge must not be one", cands, err)
	}
	// Even handed the candidate directly, the write refuses.
	n, err := st.ApplyEnergy(ctx, e.uid, receipts.EnergyCand{ChargeID: chargeID, TxnID: "tx-imp"},
		receipts.EnergyUpdate{Energy: kwh, AvgPower: 90, Meta: map[string]any{"energy_source": receipts.SourceReceipt}, ExpectedNow: c.EnergyAddedKWh})
	if err != nil || n {
		t.Fatalf("ApplyEnergy on an imported charge = %v, %v", n, err)
	}
	var energy float64
	if err := e.pool.QueryRow(`SELECT energy_added_kwh::float8 FROM charges WHERE id = $1`, chargeID).Scan(&energy); err != nil || energy != c.EnergyAddedKWh {
		t.Fatalf("energy = %v, %v, want the imported %v", energy, err, c.EnergyAddedKWh)
	}
}
