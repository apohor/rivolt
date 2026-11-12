//go:build integration

package firstscreen

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// An empty account is empty (no receipts block read when not opted in); with receipts the totals are per currency, a home session with no
// price adds nothing to billed, the top vendors and cities are the most frequent, and another user's rows never show.
func TestFirstScreenReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "fs-a")
	b, _ := db.EnsureUser(ctx, pool, "fs-b")
	s := New(pool)

	d, err := s.FirstScreen(ctx, a, false)
	if err != nil || d.Vehicles != 0 || d.OdometerMi != nil || d.Receipts.State != "off" || d.Receipts.OptIn || d.Recorded.Charges != 0 {
		t.Fatalf("empty account: %+v %v", d, err)
	}
	// Opted in, nothing synced yet: waiting.
	if d, _ = s.FirstScreen(ctx, a, true); d.Receipts.State != "waiting" {
		t.Errorf("state %q, want waiting", d.Receipts.State)
	}
	ins := func(uid any, txn, cur string, cost any, kwh float64, home bool, vendor, city, when string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, total_cost, currency, energy_kwh, is_home_charger, vendor, city)
			VALUES ($1, $2, $3::timestamptz, $3::timestamptz + interval '1 hour', $4, $5, $6, $7, $8, $9)`, uid, txn, when, cost, cur, kwh, home, vendor, city); err != nil {
			t.Fatal(err)
		}
	}
	ins(a, "t1", "USD", 12.5, 30, false, "RAN", "Albany", "2026-09-03T10:00:00Z")
	ins(a, "t2", "USD", 8.0, 20, false, "RAN", "Albany", "2026-10-01T10:00:00Z")
	ins(a, "t3", "USD", 0, 40, true, "Home", "", "2026-10-05T10:00:00Z")
	ins(a, "t4", "CAD", 5.0, 10, false, "EA", "Toronto", "2026-10-06T10:00:00Z")
	ins(b, "t9", "USD", 99, 99, false, "Other", "Elsewhere", "2026-01-01T10:00:00Z")
	d, err = s.FirstScreen(ctx, a, true)
	if err != nil {
		t.Fatal(err)
	}
	r := d.Receipts
	if r.State != "ready" || r.Sessions != 4 || r.FirstMonth != "2026-09" || len(r.Currencies) != 2 {
		t.Fatalf("receipts: %+v", r)
	}
	if u := r.Currencies[0]; u.Currency != "USD" || u.Sessions != 3 || u.HomeSessions != 1 || u.Billed != 20.5 || u.KWh != 90 {
		t.Errorf("USD: %+v", u)
	}
	if len(r.TopVendors) == 0 || r.TopVendors[0].Name != "RAN" || r.TopVendors[0].Sessions != 2 || len(r.TopCities) == 0 || r.TopCities[0].Name != "Albany" {
		t.Errorf("tops: %+v %+v", r.TopVendors, r.TopCities)
	}
	for _, v := range r.TopVendors {
		if v.Name == "Other" {
			t.Error("another user's receipt shows")
		}
	}
	// A first pass that finished with nothing to show: empty.
	c, _ := db.EnsureUser(ctx, pool, "fs-c")
	if _, err := pool.ExecContext(ctx, `INSERT INTO rivian_receipt_sync (user_id, last_attempt_at, last_summary_at, last_success_at) VALUES ($1, now(), now(), now())`, c); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.FirstScreen(ctx, c, true); d.Receipts.State != "empty" {
		t.Errorf("state %q, want empty", d.Receipts.State)
	}
}
