package entitlements

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// The founding year: every account that exists at launch gets Pro free for a year from launch, no card, nothing auto-charges, no action
// by the user (a row with source founding and an end). The grant is one idempotent statement, run from `rivolt entitlements-founding`
// with a dry run first, and it checks its own work: after it, founding rows = accounts that existed at launch - the demo account.

// FoundingEnd is how long the founding grant lasts from launch.
func FoundingEnd(launch time.Time) time.Time { return launch.UTC().AddDate(1, 0, 0) }

// FoundingReport is what the grant would do, or did.
type FoundingReport struct {
	Eligible int // accounts that existed at launch, the demo excluded
	Already  int // of those, how many already have a founding row
	ToGrant  int
	IDs      []uuid.UUID // accounts to grant, sorted
}

// IDsHash is a sha256 over the sorted ids to grant: the dry run prints it and --apply must be given it, so the run that applies is the
// run that was reviewed.
func (r FoundingReport) IDsHash() string {
	h := sha256.New()
	for _, id := range r.IDs {
		h.Write([]byte(id.String() + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// querier is the slice of *sql.DB / *sql.Tx the grant reads with.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PlanFounding lists what the founding grant would do for launch. Pure read.
func PlanFounding(ctx context.Context, q querier, demo uuid.UUID, launch time.Time) (FoundingReport, error) {
	var r FoundingReport
	rows, err := q.QueryContext(ctx, `
		SELECT u.id, (e.user_id IS NOT NULL)
		  FROM users u
		  LEFT JOIN entitlements e ON e.user_id = u.id AND e.source = 'founding'
		 WHERE u.created_at <= $1 AND u.id <> $2`, launch.UTC(), demo)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var has bool
		if err := rows.Scan(&id, &has); err != nil {
			return r, err
		}
		r.Eligible++
		if has {
			r.Already++
		} else {
			r.IDs = append(r.IDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	sort.Slice(r.IDs, func(i, j int) bool { return r.IDs[i].String() < r.IDs[j].String() })
	r.ToGrant = len(r.IDs)
	return r, nil
}

// ErrFoundingCount is the count check failing: founding rows are not accounts-at-launch minus the demo.
var ErrFoundingCount = errors.New("entitlements: founding rows do not equal the accounts that existed at launch (demo excluded)")

// ApplyFounding grants the founding year to every account that existed at launch (the demo excluded) in one transaction, then checks
// that founding rows = those accounts, and rolls back if not. Idempotent: an account that already has a founding row keeps it (its end is
// not moved), so a re-run grants nothing. It returns the number of rows it wrote.
func ApplyFounding(ctx context.Context, d *sql.DB, demo uuid.UUID, launch time.Time) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx, `
		INSERT INTO entitlements (user_id, source, status, current_period_end)
		SELECT id, 'founding', 'active', $3 FROM users WHERE created_at <= $1 AND id <> $2
		ON CONFLICT (user_id, source) DO NOTHING`, launch.UTC(), demo, FoundingEnd(launch))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	var accounts, grants int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE created_at <= $1 AND id <> $2`, launch.UTC(), demo).Scan(&accounts); err != nil {
		return 0, err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM entitlements e JOIN users u ON u.id = e.user_id
		 WHERE e.source = 'founding' AND u.created_at <= $1 AND u.id <> $2`, launch.UTC(), demo).Scan(&grants); err != nil {
		return 0, err
	}
	if accounts != grants {
		return 0, fmt.Errorf("%w: %d founding rows, %d accounts", ErrFoundingCount, grants, accounts)
	}
	return n, tx.Commit()
}
