package engage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
)

type pgStore struct{ db *sql.DB }

// NewStore returns the Postgres store.
func NewStore(db *sql.DB) Store { return &pgStore{db: db} }

func (s *pgStore) Timezone(ctx context.Context, uid uuid.UUID) (string, bool, bool, error) {
	var tz sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT timezone FROM engagement_prefs WHERE user_id = $1`, uid).Scan(&tz)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, false, err
	}
	if tz.Valid && tz.String != "" {
		return tz.String, true, true, nil
	}
	// Not stored by the browser: the statement email's zone is a fair guess for
	// quiet hours (never for a utility, which checks `stored`).
	var st sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT timezone FROM statement_email_prefs WHERE user_id = $1`, uid).Scan(&st)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, false, err
	}
	if st.Valid && st.String != "" {
		return st.String, false, true, nil
	}
	return "UTC", false, false, nil
}

func (s *pgStore) BudgetUsed(ctx context.Context, uid uuid.UUID, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM engagement_sends
		 WHERE user_id = $1 AND budgeted AND status IN ('sending', 'sent') AND claimed_at > $2::timestamptz`,
		uid, since.UTC()).Scan(&n)
	return n, err
}

// leaseClause is the guard added to a claim when the context carries a fence token (the send is
// triggered by the owner of the account's group): the claim insert itself carries the epoch, so a
// pod that lost the group cannot claim, and the claim table's unique key then keeps the new owner's
// own claim from being blocked by a stale one. n is the first free parameter number.
func leaseClause(ctx context.Context, n int) (string, []any) {
	tok, ok := fence.From(ctx)
	if !ok {
		return "", nil
	}
	return fmt.Sprintf("AND rivolt_lease_ok($%d::uuid, $%d::text, $%d::bigint)", n, n+1, n+2), []any{tok.Group, tok.Pod, tok.Epoch}
}

// leaseGone reports whether the guarded claim was refused because the lease is gone (rather than
// because the row already exists or the budget is spent).
func leaseGone(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	tok, ok := fence.From(ctx)
	if !ok {
		return false, nil
	}
	var ok2 bool
	if err := q.QueryRowContext(ctx, `SELECT rivolt_lease_ok($1::uuid, $2::text, $3::bigint)`, tok.Group, tok.Pod, tok.Epoch).Scan(&ok2); err != nil {
		return false, err
	}
	return !ok2, nil
}

func (s *pgStore) Claim(ctx context.Context, m Msg, now, since time.Time, limit int) (bool, bool, error) {
	// The budget check and the insert must not interleave with another claim for
	// the same account (two transactions can each count 3 and both insert), so a
	// per-account advisory lock serializes them for the length of the transaction.
	var tx *sql.Tx
	var err error
	if _, guarded := fence.From(ctx); guarded {
		// A guarded claim is bounded like every guarded write: a claim that passed the lease
		// check and then waits (on the advisory lock) is cancelled after 2 s.
		var cancel context.CancelFunc
		tx, ctx, cancel, err = fence.BeginBounded(ctx, s.db)
		if err == nil {
			defer cancel()
		}
	} else {
		tx, err = s.db.BeginTx(ctx, nil)
	}
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('engage:' || $1::text, 0))`, m.User); err != nil {
		return false, false, err
	}
	lease, leaseArgs := leaseClause(ctx, 9)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO engagement_sends (user_id, kind, key, channel, status, budgeted, claimed_at)
		SELECT $1, $2, $3, $4, 'sending', $5::boolean, $6::timestamptz
		 WHERE (NOT $5::boolean
		    OR (SELECT count(*) FROM engagement_sends
		         WHERE user_id = $1 AND budgeted AND status IN ('sending', 'sent') AND claimed_at > $7::timestamptz) < $8)
		   `+lease+`
		ON CONFLICT (user_id, kind, key) DO NOTHING`,
		append([]any{m.User, m.Kind, m.Key, string(m.Channel), !m.Utility, now.UTC(), since.UTC(), limit}, leaseArgs...)...)
	if err != nil {
		return false, false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, false, tx.Commit()
	}
	if gone, err := leaseGone(ctx, tx); err != nil {
		return false, false, err
	} else if gone {
		return false, false, fence.ErrFenced
	}
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM engagement_sends WHERE user_id = $1 AND kind = $2 AND key = $3)`,
		m.User, m.Kind, m.Key).Scan(&exists); err != nil {
		return false, false, err
	}
	return false, !exists, tx.Commit()
}

func (s *pgStore) Mark(ctx context.Context, uid uuid.UUID, kind, key, status string, now time.Time) error {
	var sentAt any
	if status == "sent" {
		sentAt = now.UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE engagement_sends SET status = $4, sent_at = $5::timestamptz
		 WHERE user_id = $1 AND kind = $2 AND key = $3 AND status = 'sending'`,
		uid, kind, key, status, sentAt)
	return err
}

func (s *pgStore) RecordSkip(ctx context.Context, uid uuid.UUID, kind, key string, ch Channel, status string, now time.Time) (bool, error) {
	if tok, guarded := fence.From(ctx); guarded {
		var created bool
		err := fence.InTx(ctx, s.db, func(tctx context.Context, tx *sql.Tx) error {
			res, err := tx.ExecContext(tctx, `
				INSERT INTO engagement_sends (user_id, kind, key, channel, status, budgeted, claimed_at)
				SELECT $1, $2, $3, $4, $5, FALSE, $6::timestamptz
				 WHERE rivolt_lease_ok($7::uuid, $8::text, $9::bigint)
				ON CONFLICT (user_id, kind, key) DO NOTHING`,
				uid, kind, key, string(ch), status, now.UTC(), tok.Group, tok.Pod, tok.Epoch)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			created = n == 1
			if !created {
				if gone, err := leaseGone(tctx, tx); err != nil {
					return err
				} else if gone {
					return fence.ErrFenced
				}
			}
			return nil
		})
		return created, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO engagement_sends (user_id, kind, key, channel, status, budgeted, claimed_at)
		VALUES ($1, $2, $3, $4, $5, FALSE, $6::timestamptz)
		ON CONFLICT (user_id, kind, key) DO NOTHING`,
		uid, kind, key, string(ch), status, now.UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *pgStore) StuckSending(ctx context.Context, d time.Duration, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM engagement_sends WHERE status = 'sending' AND claimed_at < $1::timestamptz`,
		now.Add(-d).UTC()).Scan(&n)
	return n, err
}

func (s *pgStore) Purge(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM engagement_sends WHERE claimed_at < $1::timestamptz`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
