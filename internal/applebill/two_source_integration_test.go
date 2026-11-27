//go:build integration

package applebill

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

// An account with a live Stripe subscription that also buys through Apple (a second device, a stale paywall) is not refused and not left
// without a record: both rows are kept, both pay, nothing of Stripe's changes, and the account counts once as paying twice.
func TestAppleKeepsLiveStripeRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "two-source")
	stripeEnd := time.Now().Add(15 * 24 * time.Hour).UTC()
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end, external_id) VALUES ($1, 'stripe', 'active', $2, 'sub_two_source')`, uid, stripeEnd); err != nil {
		t.Fatal(err)
	}
	base, err := entitlements.ReadStats(ctx, pool, db.UserIDFor(db.DemoUsername))
	if err != nil {
		t.Fatal(err)
	}
	chain := newChain(t, chainOpts{})
	txn := &Transaction{DB: pool, Verifier: chain.verifier, Config: testConfig()}
	b, _ := json.Marshal(map[string]string{"signedTransaction": chain.sign(t, ti("two-source-orig", time.Now().Add(30*24*time.Hour), uid.String()))})
	rec := httptest.NewRecorder()
	txn.Serve(uid, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(string(b))))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pro":true`) {
		t.Fatalf("apple purchase on a Stripe payer: %d %s", rec.Code, rec.Body.String())
	}
	var stripeStatus, appleStatus string
	var gotEnd time.Time
	if err := pool.QueryRowContext(ctx, `SELECT status, current_period_end FROM entitlements WHERE user_id = $1 AND source = 'stripe'`, uid).Scan(&stripeStatus, &gotEnd); err != nil {
		t.Fatal(err)
	}
	if stripeStatus != "active" || gotEnd.Sub(stripeEnd).Abs() > time.Second {
		t.Errorf("the Stripe row changed: %s %v", stripeStatus, gotEnd)
	}
	if err := pool.QueryRowContext(ctx, `SELECT status FROM entitlements WHERE user_id = $1 AND source = 'apple'`, uid).Scan(&appleStatus); err != nil || appleStatus != "active" {
		t.Errorf("apple row: %q %v", appleStatus, err)
	}
	got, err := entitlements.ReadStats(ctx, pool, db.UserIDFor(db.DemoUsername))
	if err != nil {
		t.Fatal(err)
	}
	if d := got.PayingTwice - base.PayingTwice; d != 1 {
		t.Errorf("paying twice grew by %d, want 1", d)
	}
}
