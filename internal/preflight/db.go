package preflight

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/entitlements"
)

// FoundingFromDB reads the founding dry run's counts in a read-only transaction: nothing can be written through it.
func FoundingFromDB(pool *sql.DB, demo uuid.UUID) func(ctx context.Context, launch time.Time) (Founding, error) {
	return func(ctx context.Context, launch time.Time) (Founding, error) {
		var f Founding
		tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return f, err
		}
		defer tx.Rollback() //nolint:errcheck // read-only, nothing to commit
		rep, err := entitlements.PlanFounding(ctx, tx, demo, launch)
		if err != nil {
			return f, err
		}
		f.Eligible, f.Already, f.ToGrant = rep.Eligible, rep.Already, rep.ToGrant
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*), count(*) FILTER (WHERE id = $1), count(*) FILTER (WHERE created_at > $2 AND id <> $1) FROM users`,
			demo, launch.UTC()).Scan(&f.Accounts, &f.Demo, &f.After); err != nil {
			return f, err
		}
		return f, nil
	}
}
