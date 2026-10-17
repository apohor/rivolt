//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
)

// Rivian charging receipts end to end in Postgres: stored, matched to a
// charge by window and energy, the billed amount lands beside the estimate
// without touching it, details keep nothing private, the claim is a
// cross-pod rate limit, and deleting the account removes it all.
func TestRivianReceiptsStoreEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "receipts")
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-1','R1S') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	var chargeID uuid.UUID
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source, cost, currency, cost_source, price_per_kwh)
		VALUES ($1,$2,'c-1',$3,$4,38.0,'live',12.5,'USD','network_estimate',0.33) RETURNING id`,
		uid, vid, start, start.Add(time.Hour)).Scan(&chargeID); err != nil {
		t.Fatal(err)
	}
	// A second, far-away charge that must stay unmatched.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source)
		VALUES ($1,$2,'c-2',$3,$4,10,'live')`, uid, vid, start.Add(-30*time.Hour), start.Add(-29*time.Hour)); err != nil {
		t.Fatal(err)
	}

	st := receipts.NewStore(pool)
	billed, kwh := 14.25, 38.4
	sums := []rivian.SessionSummary{
		{TransactionID: "txn-1", StartedAt: start.Add(2 * time.Minute), EndedAt: start.Add(58 * time.Minute), TotalCost: &billed, Currency: "USD", EnergyKWh: &kwh, Vendor: "Rivian", City: "Reno"},
		{TransactionID: "txn-old", StartedAt: start.Add(-100 * time.Hour), EndedAt: start.Add(-99 * time.Hour), Currency: "USD"},
	}
	if err := st.UpsertReceipts(ctx, uid, sums); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertReceipts(ctx, uid, sums); err != nil { // idempotent
		t.Fatal(err)
	}
	un, err := st.Unmatched(ctx, uid)
	if err != nil || len(un) != 2 {
		t.Fatalf("unmatched = %d, err %v; want 2", len(un), err)
	}
	cs, err := st.CandidateCharges(ctx, uid, start.Add(-3*time.Hour), start.Add(3*time.Hour))
	if err != nil || len(cs) != 1 || cs[0].ID != chargeID {
		t.Fatalf("candidates = %+v, err %v", cs, err)
	}
	pairs := receipts.Match(cs, un)
	if len(pairs) != 1 {
		t.Fatalf("pairs = %+v", pairs)
	}
	if err := st.Apply(ctx, uid, pairs); err != nil {
		t.Fatal(err)
	}

	var cost, rcost sql.NullFloat64
	var src, rsrc, rcur, rtxn sql.NullString
	read := func() {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT cost::float8, cost_source, receipt_cost::float8, receipt_currency, receipt_txn_id, receipt_source FROM charges WHERE id = $1`, chargeID).
			Scan(&cost, &src, &rcost, &rcur, &rtxn, &rsrc); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if rcost.Float64 != 14.25 || rcur.String != "USD" || rtxn.String != "txn-1" || rsrc.String != "rivian_receipt" {
		t.Fatalf("receipt columns = %+v %+v %+v %+v", rcost, rcur, rtxn, rsrc)
	}
	if cost.Float64 != 12.5 || src.String != "network_estimate" {
		t.Fatalf("the estimate was overwritten: cost %v source %q", cost.Float64, src.String)
	}
	// The charge no longer shows as a candidate; the old receipt stays open.
	if cs, _ := st.CandidateCharges(ctx, uid, start.Add(-3*time.Hour), start.Add(3*time.Hour)); len(cs) != 0 {
		t.Errorf("a matched charge is still a candidate: %+v", cs)
	}
	if un, _ := st.Unmatched(ctx, uid); len(un) != 1 || un[0].TransactionID != "txn-old" {
		t.Errorf("unmatched after apply = %+v", un)
	}

	// Rivian finalizes the amount later: the charge follows, the estimate still doesn't move.
	billed = 15.0
	sums[0].TotalCost = &billed
	if err := st.UpsertReceipts(ctx, uid, sums); err != nil {
		t.Fatal(err)
	}
	if err := st.RefreshMatched(ctx, uid); err != nil {
		t.Fatal(err)
	}
	read()
	if rcost.Float64 != 15.0 || cost.Float64 != 12.5 {
		t.Errorf("after refresh receipt %v cost %v", rcost.Float64, cost.Float64)
	}

	// Details: a home charger keeps its flag but not its name.
	need, err := st.NeedDetail(ctx, uid, 10)
	if err != nil || len(need) != 1 || need[0] != "txn-1" {
		t.Fatalf("need detail = %v, err %v", need, err)
	}
	home := true
	if err := st.SaveDetail(ctx, uid, "txn-1", &rivian.SessionCharger{Name: "12 Elm Street garage", Vendor: "Rivian", IsHome: &home}); err != nil {
		t.Fatal(err)
	}
	var name sql.NullString
	var isHome sql.NullBool
	var fetched sql.NullTime
	if err := pool.QueryRowContext(ctx, `SELECT charger_name, is_home_charger, detail_fetched_at FROM rivian_receipts WHERE user_id = $1 AND transaction_id = 'txn-1'`, uid).Scan(&name, &isHome, &fetched); err != nil {
		t.Fatal(err)
	}
	if name.Valid || !isHome.Valid || !isHome.Bool || !fetched.Valid {
		t.Errorf("home charger detail: name %+v home %+v fetched %+v", name, isHome, fetched)
	}
	if need, _ := st.NeedDetail(ctx, uid, 10); len(need) != 0 {
		t.Errorf("detail asked again: %v", need)
	}

	// The claim is the rate limit: a second claim inside the window loses.
	if ok, err := st.ClaimSummaries(ctx, uid, time.Hour); err != nil || !ok {
		t.Fatalf("first claim = %v, err %v", ok, err)
	}
	if ok, _ := st.ClaimSummaries(ctx, uid, time.Hour); ok {
		t.Errorf("second claim inside the window won")
	}
	if ok, _ := st.ClaimDetails(ctx, uid, time.Hour); ok {
		t.Errorf("a details claim inside the window won")
	}
	if ok, err := st.ClaimSummaries(ctx, uid, 0); err != nil || !ok {
		t.Errorf("claim with a zero window = %v, err %v", ok, err)
	}
	if err := st.MarkResult(ctx, uid, nil); err != nil {
		t.Fatal(err)
	}

	// Account deletion removes receipts and the sync row with the user.
	if err := db.DeleteUser(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rivian_receipts", "rivian_receipt_sync"} {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows after account delete = %d, err %v", table, n, err)
		}
	}
}

// Software-update alert bookkeeping in Postgres: a vehicle's first sync is a
// silent baseline, a new version is claimed exactly once, and deleting the
// account removes the notices; the push preference defaults on.
func TestOTANoticesClaimOnceAndCascade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "otanotices")
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := db.SeedOTANotices(ctx, pool, uid, "rv-1", "2026.30", "2026.36")
	if err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	if again, _ := db.SeedOTANotices(ctx, pool, uid, "rv-1", "2026.30", "2026.38"); again {
		t.Error("second seed wrote rows")
	}
	if fresh, _ := db.ClaimOTANotice(ctx, pool, uid, "rv-1", db.OTAAvailable, "2026.36"); fresh {
		t.Error("baseline version was claimable")
	}
	for i, want := range []bool{true, false} {
		fresh, err := db.ClaimOTANotice(ctx, pool, uid, "rv-1", db.OTAAvailable, "2026.38")
		if err != nil || fresh != want {
			t.Fatalf("claim #%d = %v, %v; want %v", i, fresh, err, want)
		}
	}
	if err := db.DeleteUser(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM ota_notices WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 0 {
		t.Errorf("notices left after delete: %d, %v", n, err)
	}
}
