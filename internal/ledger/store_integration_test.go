//go:build integration

// Ledger loader integration test: NULL handling, window and vehicle
// filters, and cross-user isolation against a real Postgres with every
// migration applied.
//
// Run with: go test -tags=integration ./internal/ledger/...
package ledger_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/ledger"
)

func openPG(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER": "rivolt", "POSTGRES_PASSWORD": "rivolt", "POSTGRES_DB": "rivolt_test",
			},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	pool, err := db.Open(ctx, fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port()))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func mustExec(t *testing.T, pool *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func vehicle(t *testing.T, pool *sql.DB, uid uuid.UUID, rivianID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id`,
		uid, rivianID).Scan(&id); err != nil {
		t.Fatalf("insert vehicle: %v", err)
	}
	return id
}

func TestLoad_NullsWindowVehicleAndIsolation(t *testing.T) {
	pool := openPG(t)
	ctx := context.Background()
	alice, err := db.EnsureUser(ctx, pool, "ledger-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "ledger-bob")
	if err != nil {
		t.Fatal(err)
	}
	v1 := vehicle(t, pool, alice, "rv-1")
	v2 := vehicle(t, pool, alice, "rv-2")
	vb := vehicle(t, pool, bob, "rv-bob")

	in := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	out := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Priced, fully populated session on rv-1.
	mustExec(t, pool, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at,
		start_soc_pct, end_soc_pct, energy_added_kwh, max_power_kw, cost, currency, price_per_kwh,
		final_state, source, lat, lon)
		VALUES ($1,$2,'c-priced',$3,$4,40,70,30.5,7.6,3.355,'USD',0.11,'charging_complete','live',30.27,-97.74)`,
		alice, v1, in, in.Add(4*time.Hour))
	// Unpriced session with NULL energy/cost/coords on rv-2.
	mustExec(t, pool, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source)
		VALUES ($1,$2,'c-null',$3,$4,'live')`, alice, v2, in.Add(time.Hour), in.Add(2*time.Hour))
	// Outside the window, and another user's row.
	mustExec(t, pool, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source)
		VALUES ($1,$2,'c-old',$3,$4,'live')`, alice, v1, out, out.Add(time.Hour))
	mustExec(t, pool, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, energy_added_kwh)
		VALUES ($1,$2,'c-bob',$3,$4,'live',99)`, bob, vb, in, in.Add(time.Hour))
	mustExec(t, pool, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source, start_soc_pct, end_soc_pct)
		VALUES ($1,$2,'d-1',$3,$4,12.5,'electrafi_import',80,74.5)`, alice, v1, in.Add(-time.Hour), in.Add(-30*time.Minute))

	p, _ := ledger.ParsePeriod("2026-10", time.UTC)
	from, to := ledger.LoadWindow(p)
	cs, ds, err := ledger.Load(ctx, pool, alice, "", from, to)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cs) != 2 || cs[0].ID != "c-priced" || cs[1].ID != "c-null" {
		t.Fatalf("charges = %+v, want c-priced, c-null", cs)
	}
	got := cs[0]
	if got.VehicleID != "rv-1" || got.Cost == nil || *got.Cost != 3.355 || got.PricePerKWh == nil ||
		*got.PricePerKWh != 0.11 || got.EnergyKWh == nil || *got.EnergyKWh != 30.5 || got.Lat == nil {
		t.Errorf("priced row = %+v", got)
	}
	n := cs[1]
	if n.Cost != nil || n.EnergyKWh != nil || n.PricePerKWh != nil || n.Lat != nil || n.StartSoC != nil {
		t.Errorf("NULL columns must load as nil: %+v", n)
	}
	if len(ds) != 1 || ds[0].DistanceMi == nil || *ds[0].DistanceMi != 12.5 || ds[0].EnergyKWh != nil ||
		ds[0].StartSoC == nil || *ds[0].StartSoC != 80 || ds[0].EndSoC == nil || *ds[0].EndSoC != 74.5 {
		t.Errorf("drives = %+v", ds)
	}

	only, _, err := ledger.Load(ctx, pool, alice, "rv-2", from, to)
	if err != nil || len(only) != 1 || only[0].ID != "c-null" {
		t.Errorf("vehicle filter = %+v, %v", only, err)
	}
	bobs, _, err := ledger.Load(ctx, pool, bob, "", from, to)
	if err != nil || len(bobs) != 1 || bobs[0].ID != "c-bob" {
		t.Errorf("bob sees %+v, %v", bobs, err)
	}

	if cs[0].CostSource != "" || cs[0].Network != "" {
		t.Errorf("before the pricing columns exist, cost_source/network must load empty: %+v", cs[0])
	}

	// Simulate migration 0040 (idempotent if it has already run) and
	// check the loader picks the columns up without a restart.
	mustExec(t, pool, `ALTER TABLE charges ADD COLUMN IF NOT EXISTS cost_source TEXT`)
	mustExec(t, pool, `ALTER TABLE charges ADD COLUMN IF NOT EXISTS network TEXT`)
	mustExec(t, pool, `UPDATE charges SET cost_source = 'network_estimate', network = 'Electrify America'
		WHERE external_id = 'c-priced'`)
	cs, ds, err = ledger.Load(ctx, pool, alice, "", from, to)
	if err != nil {
		t.Fatalf("Load after 0040: %v", err)
	}
	if cs[0].CostSource != "network_estimate" || cs[0].Network != "Electrify America" || cs[1].CostSource != "" {
		t.Errorf("pricing columns: %q/%q, legacy %q", cs[0].CostSource, cs[0].Network, cs[1].CostSource)
	}

	m := ledger.Build(ledger.Input{Charges: cs, Drives: ds,
		Config: ledger.Config{Currency: "USD", HomeRate: 0.11}, Now: in}, p, true)
	if m.Spend.Total != 3.355 || m.Spend.BySource["network_estimate"] != 3.355 ||
		// c-null has no recorded energy: a crumb, counted as null_energy
		// and not as "needs a price" (A5 / D2 F2).
		m.Spend.UnpricedSessions != 0 || m.Energy.NullEnergySessions != 1 {
		t.Errorf("built from DB rows: spend %+v energy %+v", m.Spend, m.Energy)
	}
}

func TestToConfirm_CountsAndIsolation(t *testing.T) {
	pool := openPG(t)
	ctx := context.Background()
	alice, err := db.EnsureUser(ctx, pool, "confirm-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "confirm-bob")
	if err != nil {
		t.Fatal(err)
	}
	va := vehicle(t, pool, alice, "rv-a")
	vb := vehicle(t, pool, bob, "rv-b")
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ins := func(uid, veh uuid.UUID, id string, at time.Time, state any, source any) {
		mustExec(t, pool, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source,
			energy_added_kwh, max_power_kw, cost, price_per_kwh, cost_source, network, network_state, network_confidence, network_evidence)
			VALUES ($1,$2,$3,$4,$5,'live',40,150,16,0.4,$7,'Tesla Supercharger',$6,0.7,'{"v":1}')`,
			uid, veh, id, at, at.Add(30*time.Minute), state, source)
	}
	// Months apart: the count and list are all-time.
	ins(alice, va, "a-est", base, "estimated", "network_estimate")
	ins(alice, va, "a-need", base.AddDate(0, 5, 0), "needs_confirmation", "network_estimate")
	ins(alice, va, "a-conf", base.AddDate(0, 2, 0), "confirmed", "network_estimate")
	ins(alice, va, "a-plain", base.AddDate(0, 3, 0), nil, "rivian")
	ins(bob, vb, "b-need", base, "needs_confirmation", "network_estimate")

	open, attention, err := ledger.CountToConfirm(ctx, pool, alice)
	if err != nil || open != 2 || attention != 1 {
		t.Errorf("counts = %d/%d, %v; want 2/1", open, attention, err)
	}
	// The window loader carries the same fields.
	p, _ := ledger.ParsePeriod("2026-03", time.UTC)
	from, to := ledger.LoadWindow(p)
	win, _, err := ledger.Load(ctx, pool, alice, "", from, to)
	if err != nil || len(win) != 1 || win[0].NetworkState != "estimated" {
		t.Errorf("window = %+v, %v", win, err)
	}
	if n, _, err := ledger.CountToConfirm(ctx, pool, bob); err != nil || n != 1 {
		t.Errorf("bob = %d, %v", n, err)
	}
}

// The loader carries what Rivian billed (charges.receipt_* plus the vendor,
// charger name and home flag of the matched rivian_receipts row), and a
// charge without a receipt loads with none.
func TestLoad_BilledFromReceipts(t *testing.T) {
	pool := openPG(t)
	ctx := context.Background()
	alice, err := db.EnsureUser(ctx, pool, "billed-alice")
	if err != nil {
		t.Fatal(err)
	}
	v := vehicle(t, pool, alice, "rv-1")
	in := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var matched, bare, home uuid.UUID
	for _, row := range []struct {
		ext string
		id  *uuid.UUID
		at  time.Time
	}{{"c-matched", &matched, in}, {"c-bare", &bare, in.Add(24 * time.Hour)}, {"c-home", &home, in.Add(48 * time.Hour)}} {
		if err := pool.QueryRow(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source, cost, currency, cost_source)
			VALUES ($1,$2,$3,$4,$5,30,'live',12.5,'USD','network_estimate') RETURNING id`, alice, v, row.ext, row.at, row.at.Add(time.Hour)).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, pool, `INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, total_cost, currency, vendor, charger_name, is_home_charger, charge_id)
		VALUES ($1,'t1',$2,$3,14.25,'USD','Electrify America','EA Reno 12',false,$4)`, alice, in, in.Add(time.Hour), matched)
	mustExec(t, pool, `UPDATE charges SET receipt_cost = 14.25, receipt_currency = 'USD', receipt_txn_id = 't1', receipt_source = 'rivian_receipt' WHERE id = $1`, matched)
	mustExec(t, pool, `INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, total_cost, currency, vendor, is_home_charger, charge_id)
		VALUES ($1,'t2',$2,$3,5.5,'USD','Rivian',true,$4)`, alice, in.Add(48*time.Hour), in.Add(49*time.Hour), home)
	mustExec(t, pool, `UPDATE charges SET receipt_cost = 5.5, receipt_currency = 'USD', receipt_txn_id = 't2', receipt_source = 'rivian_receipt' WHERE id = $1`, home)

	// A second receipt that also points at the matched charge (matching
	// misbehaving, a manual fix, a replay). The charge must still load once,
	// with the receipt its own receipt_txn_id names.
	mustExec(t, pool, `INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, total_cost, currency, vendor, charger_name, is_home_charger, charge_id)
		VALUES ($1,'t1-dup',$2,$3,99,'USD','Someone Else','Other Station',false,$4)`, alice, in, in.Add(time.Hour), matched)

	p, _ := ledger.ParsePeriod("2026-10", time.UTC)
	from, to := ledger.LoadWindow(p)
	cs, _, err := ledger.Load(ctx, pool, alice, "", from, to)
	if err != nil || len(cs) != 3 {
		t.Fatalf("Load = %d rows, err %v; a second receipt on the same charge must not duplicate it", len(cs), err)
	}
	by := map[string]ledger.Charge{}
	for _, c := range cs {
		by[c.ID] = c
	}
	if m := by["c-matched"]; m.BilledVendor == "Someone Else" {
		t.Errorf("the duplicate receipt was joined: %+v", m)
	}
	if m := by["c-matched"]; m.BilledCost == nil || *m.BilledCost != 14.25 || m.BilledCurrency != "USD" || m.BilledVendor != "Electrify America" || m.BilledCharger != "EA Reno 12" || m.BilledHome {
		t.Errorf("matched = %+v", m)
	}
	if b := by["c-bare"]; b.BilledCost != nil || b.BilledVendor != "" || b.BilledCurrency != "" {
		t.Errorf("a charge without a receipt loaded billed fields: %+v", b)
	}
	if h := by["c-home"]; h.BilledCost == nil || *h.BilledCost != 5.5 || !h.BilledHome {
		t.Errorf("home = %+v", h)
	}
	// And the statement built from them (flag on): billed beside the estimate.
	m := ledger.Build(ledger.Input{Charges: cs, Config: ledger.Config{Currency: "USD", HomeRate: 0.11, Billed: true}, Now: in.Add(72 * time.Hour)}, p, true)
	if m.Spend.BilledTotal == nil || *m.Spend.BilledTotal != 19.75 || *m.BilledSessions != 2 || *m.EstimatedSessions != 1 {
		t.Errorf("statement billed total %v sessions %v/%v", m.Spend.BilledTotal, m.BilledSessions, m.EstimatedSessions)
	}
	// Two billed sessions (19.75) plus the one estimated session (12.5): the headline is their sum.
	if m.Spend.EstimatedTotal == nil || *m.Spend.EstimatedTotal != 12.5 || m.Spend.Total != 32.25 {
		t.Errorf("estimated_total %v, spend.total %v; want 12.5 and 32.25", m.Spend.EstimatedTotal, m.Spend.Total)
	}
}
