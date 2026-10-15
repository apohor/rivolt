//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// TestAccountDeleteClearsChargeBackups: charges_deleted_backup has no FK
// to users, so deleting an account (DeleteUserAccount, and DeleteUser's
// rollback path) must remove that user's repair backups itself, and only
// theirs.
func TestAccountDeleteClearsChargeBackups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	backup := func(uid uuid.UUID, kind string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO charges_deleted_backup (batch_id, kind, charge_id, user_id, row, reason)
			VALUES ($1, $2, $3, $4, '{}'::jsonb, 'test')`,
			uuid.New(), kind, uuid.New(), uid); err != nil {
			t.Fatalf("seed backup: %v", err)
		}
	}
	count := func(uid uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges_deleted_backup WHERE user_id = $1`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	gone, err := db.EnsureUser(ctx, pool, "backup-deleted")
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := db.EnsureUser(ctx, pool, "backup-rollback")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := db.EnsureUser(ctx, pool, "backup-kept")
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uuid.UUID{gone, rolledBack, kept} {
		backup(uid, "charge")
		backup(uid, "pack_health_sample")
		backup(uid, "charge_restamp")
	}

	if err := db.DeleteUserAccount(ctx, pool, gone); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(ctx, pool, rolledBack); err != nil {
		t.Fatal(err)
	}
	if n := count(gone); n != 0 {
		t.Errorf("DeleteUserAccount left %d backup rows", n)
	}
	if n := count(rolledBack); n != 0 {
		t.Errorf("DeleteUser left %d backup rows", n)
	}
	if n := count(kept); n != 3 {
		t.Errorf("another user's backups: %d rows, want 3", n)
	}
}
