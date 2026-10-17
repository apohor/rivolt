//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/signuprequests"
)

// Retention and consent in Postgres: sessions are removed 30 days after
// they expire, waitlist requests by their rule, the receipts opt-in is off
// by default, and opting out deletes what the sync stored.
func TestPrivacyRetentionAndReceiptsOptIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "privacy")
	if err != nil {
		t.Fatal(err)
	}

	// Sessions: expired 31 days ago goes, expired 29 days ago and live stay.
	for name, exp := range map[string]string{"old": "now() - interval '31 days'", "recent": "now() - interval '29 days'", "live": "now() + interval '1 day'"} {
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, expires_at, user_agent, ip_address)
			VALUES ($1, $2, $3, now() - interval '60 days', now() - interval '40 days', `+exp+`, 'ua', '203.0.113.9')`,
			uuid.New(), uid, []byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := sessions.New(pool, []byte("pepper-pepper-pepper-pepper-1234"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := ss.PurgeExpired(ctx, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("sessions purged = %d, %v; want 1", n, err)
	}

	// Waitlist.
	day := 24 * time.Hour
	at := func(d time.Duration) *time.Time { t := time.Now().Add(-d); return &t }
	ins := func(email, status string, requested time.Duration, used, expires *time.Time) {
		t.Helper()
		var token *string
		var decided *time.Time
		if status == "approved" {
			token, decided = &email, at(requested)
		}
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO signup_requests (email, status, requested_at, decided_at, signup_token, token_expires_at, token_used_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			email, status, time.Now().Add(-requested), decided, token, expires, used); err != nil {
			t.Fatal(err)
		}
	}
	ins("pending-old@x.test", "pending", 91*day, nil, nil)
	ins("pending-new@x.test", "pending", 10*day, nil, nil)
	ins("rejected-old@x.test", "rejected", 100*day, nil, nil)
	ins("rejected-new@x.test", "rejected", 20*day, nil, nil)
	ins("approved-used@x.test", "approved", 5*day, at(day), at(-2*day))
	ins("approved-waiting@x.test", "approved", 5*day, nil, at(-2*day))
	ins("approved-lapsed@x.test", "approved", 80*day, nil, at(40*day))
	n, err := signuprequests.New(pool).PurgeStale(ctx)
	if err != nil || n != 4 {
		t.Fatalf("waitlist purged = %d, %v; want 4 (pending-old, rejected-old, approved-used, approved-lapsed)", n, err)
	}
	var left int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM signup_requests WHERE email IN ('pending-new@x.test','rejected-new@x.test','approved-waiting@x.test')`).Scan(&left)
	if left != 3 {
		t.Errorf("kept %d of the 3 requests that should stay", left)
	}

	// Receipts opt-in: off by default, on after Set, and opting out deletes.
	st, _ := settings.OpenStore(pool, uid)
	if settings.RivianReceiptsOptIn(ctx, st) {
		t.Fatal("opt-in is on by default")
	}
	if err := settings.SetRivianReceiptsOptIn(ctx, st, true); err != nil || !settings.RivianReceiptsOptIn(ctx, st) {
		t.Fatalf("opt-in not saved: %v", err)
	}
	var vid, cid uuid.UUID
	_ = pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-p') RETURNING id`, uid).Scan(&vid)
	if err := pool.QueryRowContext(ctx, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source, receipt_cost, receipt_currency, receipt_txn_id, receipt_source)
		VALUES ($1,$2,'c-p',now()-interval '2 hours',now()-interval '1 hour',30,'live',12.5,'USD','t1','rivian_receipt') RETURNING id`, uid, vid).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, currency, charge_id) VALUES ($1,'t1',now()-interval '2 hours',now()-interval '1 hour','USD',$2)`, uid, cid); err != nil {
		t.Fatal(err)
	}
	if err := receipts.NewStore(pool).Purge(ctx, uid); err != nil {
		t.Fatal(err)
	}
	var rc, cost int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM rivian_receipts WHERE user_id = $1`, uid).Scan(&rc)
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM charges WHERE id = $1 AND receipt_cost IS NULL AND receipt_txn_id IS NULL AND energy_added_kwh = 30`, cid).Scan(&cost)
	if rc != 0 || cost != 1 {
		t.Errorf("after purge: receipts %d, charge cleared %d (want 0, 1)", rc, cost)
	}
}
