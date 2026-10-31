// Package histfloor is the "history deleted before" floor: when a user deletes their history (all of it, or everything before a month),
// no later writer may bring that history back. Rivian still has the receipts and a catch-up still has the old samples, so without a floor
// the receipts syncer, the replay catch-up and the drive and charge writers would re-insert what the user just deleted.
//
// The floor is one timestamp per user (users.history_deleted_before). It only ever rises. Writers that bring in data by itself, not
// because the user asked, drop any row that starts before it. A user's own import or restore is explicit and passes Bypass: it is the
// user choosing to put their data back. The floor is never shown or exported, and it goes with the account (it is a column of users).
package histfloor

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

type bypassKey struct{}

// Bypass marks ctx as a user-started import or restore: the user is putting their own data back, so the floor does not apply.
func Bypass(ctx context.Context) context.Context { return context.WithValue(ctx, bypassKey{}, true) }

func bypassed(ctx context.Context) bool { v, _ := ctx.Value(bypassKey{}).(bool); return v }

// Querier is the slice of *sql.DB / *sql.Tx the floor needs.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Get returns uid's floor, the zero time when there is none (and for a bypassed ctx or a nil database). An error is returned, never
// swallowed: a writer that cannot read the floor must not write what the floor may forbid.
func Get(ctx context.Context, q Querier, uid uuid.UUID) (time.Time, error) {
	if q == nil || uid == uuid.Nil || bypassed(ctx) {
		return time.Time{}, nil
	}
	var f sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT history_deleted_before FROM users WHERE id = $1`, uid).Scan(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if !f.Valid {
		return time.Time{}, nil
	}
	return f.Time, nil
}

// Below reports whether a row that starts at t is before uid's floor and so must be dropped.
func Below(ctx context.Context, q Querier, uid uuid.UUID, t time.Time) (bool, error) {
	f, err := Get(ctx, q, uid)
	if err != nil || f.IsZero() {
		return false, err
	}
	return t.Before(f), nil
}

// Execer is the slice of *sql.DB / *sql.Tx Raise needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Raise lifts uid's floor to t. It never lowers it. Call it BEFORE the deletes it protects: a writer that reads the floor after that
// point cannot re-insert what the deletes are about to remove.
func Raise(ctx context.Context, e Execer, uid uuid.UUID, t time.Time) error {
	if e == nil || uid == uuid.Nil {
		return nil
	}
	_, err := e.ExecContext(ctx, `
		UPDATE users SET history_deleted_before = t.v
		  FROM (SELECT $2::timestamptz AS v) t
		 WHERE users.id = $1 AND (users.history_deleted_before IS NULL OR users.history_deleted_before < t.v)`, uid, t.UTC())
	return err
}

// Who is and is not held to the floor.
//
//   - Held: the receipts syncer (UpsertReceipts), the drive and charge writers (Upsert: the live close, and any other caller without
//     Bypass) and replay catch-up (samples InsertBulk). A drive or charge that STARTED before the floor is dropped whole, not cut:
//     half a drive would be a different drive, and an open one that crossed the floor is deleted history by its start. Samples are cut
//     per row. archive.Restorer refuses a month wholly before the floor.
//   - Not held: the live recorder's one-sample InsertBatch (always taken now, after any floor); a user's own import and restore
//     (Bypass, set by those handlers); the operator's repair CLIs, which write with SQL or Bypass and are run on purpose
//     (phantom-charge restore, route-gap backfill, R2 repairs): an operator who restores deleted rows has decided to.
