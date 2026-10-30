//go:build integration

package fence_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/testpg"
)

// A guarded Exec handed a *sql.DB runs in its own bounded transaction: the statement sees the 2 s
// statement and 1 s idle-in-transaction timeouts, and one that passed the lease check and then
// waits on a row lock is cancelled within the cap instead of committing whenever the lock frees.
// Unguarded it is the plain autocommit statement, as before.
func TestExec_GuardedOnADBIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "fenceexec")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `CREATE TABLE fence_probe (k int PRIMARY KEY, st text, idle text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO fence_probe VALUES (1, '', '')`); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	owner := fence.With(ctx, fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epoch})
	const probe = `UPDATE fence_probe SET st = current_setting('statement_timeout'),
		idle = current_setting('idle_in_transaction_session_timeout') WHERE k = $1 AND {{ok}}`
	settings := func() (string, string) {
		var st, idle string
		if err := pool.QueryRowContext(ctx, `SELECT st, idle FROM fence_probe WHERE k = 1`).Scan(&st, &idle); err != nil {
			t.Fatal(err)
		}
		return st, idle
	}

	if n, err := fence.Exec(owner, pool, probe, 1); err != nil || n != 1 {
		t.Fatalf("guarded = %d, %v; want 1 row", n, err)
	}
	if st, idle := settings(); st != "2s" || idle != "1s" {
		t.Errorf("guarded on a *sql.DB ran with statement_timeout %q, idle %q; want 2s, 1s", st, idle)
	}
	if n, err := fence.Exec(ctx, pool, probe, 1); err != nil || n != 1 {
		t.Fatalf("plain = %d, %v; want 1 row", n, err)
	}
	if st, _ := settings(); st == "2s" {
		t.Errorf("plain Exec ran with the guarded statement_timeout")
	}

	// Another session holds the row: the guarded write passes the lease check, waits on the lock,
	// and is cancelled within the cap; the row is untouched once the holder lets go.
	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := holder.ExecContext(ctx, `SELECT 1 FROM fence_probe WHERE k = 1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = fence.Exec(owner, pool, `UPDATE fence_probe SET st = 'late' WHERE k = $1 AND {{ok}}`, 1)
	took := time.Since(start)
	if err == nil {
		t.Fatal("guarded write behind a held row lock succeeded; want it cancelled")
	}
	if took > fence.TxCap+time.Second {
		t.Errorf("guarded write waited %v on the lock; want it cancelled within %v", took, fence.TxCap)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if st, _ := settings(); st == "late" {
		t.Error("the cancelled write committed after the lock was released")
	}
}
