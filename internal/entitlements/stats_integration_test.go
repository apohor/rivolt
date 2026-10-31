//go:build integration

package entitlements_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

// The dashboard's counts: entitlement rows by source and status, and the accounts, with the demo account left out of both; nothing in them
// names a person.
func TestReadStats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	base, err := entitlements.ReadStats(ctx, pool, db.UserIDFor(db.DemoUsername))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := db.EnsureUser(ctx, pool, "stats-a")
	b, _ := db.EnsureUser(ctx, pool, "stats-b")
	demo, _ := db.EnsureUserFull(ctx, pool, db.DemoUsername, "", "Demo")
	for _, e := range []struct {
		uid                 interface{}
		source, status, end string
	}{{a, "founding", "active", "now() + interval '1 year'"}, {b, "stripe", "past_due", "now() + interval '30 days'"}, {demo, "founding", "active", "now() + interval '1 year'"}} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, $2, $3, `+e.end+`)`, e.uid, e.source, e.status); err != nil {
			t.Fatal(err)
		}
	}
	got, err := entitlements.ReadStats(ctx, pool, db.UserIDFor(db.DemoUsername))
	if err != nil {
		t.Fatal(err)
	}
	if got.Accounts != base.Accounts+2 {
		t.Errorf("accounts %d, want %d (the demo is left out)", got.Accounts, base.Accounts+2)
	}
	if d := got.ByKind[[2]string{"founding", "active"}] - base.ByKind[[2]string{"founding", "active"}]; d != 1 {
		t.Errorf("founding/active grew by %d, want 1 (the demo's row is left out)", d)
	}
	if d := got.ByKind[[2]string{"stripe", "past_due"}] - base.ByKind[[2]string{"stripe", "past_due"}]; d != 1 {
		t.Errorf("stripe/past_due grew by %d, want 1", d)
	}
}
