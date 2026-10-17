//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/receipts"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// A receipts sync can't write after an opt-out: a write waiting on the
// opt-in row sees the opt-out once it commits and writes nothing, writes
// after it are refused, and the retention sweep deletes whatever a user who
// isn't opted in still has.
func TestReceiptsOptOutRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	user := func(name string) uuid.UUID {
		t.Helper()
		id, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	uid, keep := user("receipts-race"), user("receipts-keep")
	setOptIn := func(u uuid.UUID, on bool) {
		t.Helper()
		ss, err := settings.OpenStore(pool, u)
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.SetRivianReceiptsOptIn(ctx, ss, on); err != nil {
			t.Fatal(err)
		}
	}
	sum := func(txn string) []rivian.SessionSummary {
		cost := 9.5
		start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
		return []rivian.SessionSummary{{TransactionID: txn, StartedAt: start, EndedAt: start.Add(time.Hour), TotalCost: &cost, Currency: "USD"}}
	}
	count := func(table string, u uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, u).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	st := receipts.NewStore(pool)

	// Never opted in: nothing is written, not even the claim.
	if err := st.UpsertReceipts(ctx, uid, sum("t0")); !errors.Is(err, receipts.ErrOptedOut) {
		t.Fatalf("write before opting in: %v, want ErrOptedOut", err)
	}
	if won, err := st.ClaimSummaries(ctx, uid, time.Hour); won || err != nil || count("rivian_receipt_sync", uid) != 0 {
		t.Fatalf("claim before opting in: won %v err %v", won, err)
	}

	setOptIn(uid, true)
	if err := st.UpsertReceipts(ctx, uid, sum("t1")); err != nil || count("rivian_receipts", uid) != 1 {
		t.Fatalf("opted-in write: %v, %d rows", err, count("rivian_receipts", uid))
	}

	// The race: an opt-out is in flight (its settings update holds the row,
	// not yet committed) while the sync writes. The write must wait for it,
	// then see the opt-out and write nothing.
	optOut, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := optOut.ExecContext(ctx, `
		UPDATE user_settings SET value = value || '{"privacy.rivian_receipts": "0"}'::jsonb
		 WHERE user_id = $1 AND value ? 'privacy.rivian_receipts'`, uid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- st.UpsertReceipts(ctx, uid, sum("t2")) }()
	select {
	case err := <-done:
		t.Fatalf("the write didn't wait for the in-flight opt-out: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := optOut.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(ctx, uid); err != nil { // as the opt-out handler does next
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, receipts.ErrOptedOut) {
		t.Fatalf("write racing the opt-out: %v, want ErrOptedOut", err)
	}
	if n := count("rivian_receipts", uid); n != 0 {
		t.Fatalf("%d receipts after the opt-out, want 0", n)
	}

	// After the opt-out every write is refused.
	if err := st.UpsertReceipts(ctx, uid, sum("t3")); !errors.Is(err, receipts.ErrOptedOut) {
		t.Errorf("write after opt-out: %v", err)
	}
	if err := st.MarkResult(ctx, uid, nil); !errors.Is(err, receipts.ErrOptedOut) {
		t.Errorf("result after opt-out: %v", err)
	}
	if won, _ := st.ClaimSummaries(ctx, uid, time.Hour); won || count("rivian_receipt_sync", uid) != 0 {
		t.Errorf("claim after opt-out: won %v, %d sync rows", won, count("rivian_receipt_sync", uid))
	}

	// Backstop: rows a user who isn't opted in still has (a failed purge,
	// say) go in the daily sweep; an opted-in user's stay.
	setOptIn(keep, true)
	if err := st.UpsertReceipts(ctx, keep, sum("k1")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO rivian_receipts (user_id, transaction_id, started_at, ended_at, total_cost, currency)
		VALUES ($1, 'slipped', now() - interval '1 day', now() - interval '23 hours', 5, 'USD')`, uid); err != nil {
		t.Fatal(err)
	}
	n, err := st.PurgeNotOptedIn(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep deleted %d, %v; want 1", n, err)
	}
	if count("rivian_receipts", uid) != 0 || count("rivian_receipts", keep) != 1 {
		t.Errorf("after the sweep: opted-out %d, opted-in %d; want 0 and 1", count("rivian_receipts", uid), count("rivian_receipts", keep))
	}
}
