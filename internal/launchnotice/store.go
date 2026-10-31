package launchnotice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ClaimStale is how long a claimed email may sit unsent before another run may claim it again (a crash between claim and send).
const ClaimStale = 15 * time.Minute

// Notice is one account's notice.
type Notice struct {
	Kind string
	// Params are the launch notice's dates (empty for other kinds); Raw is the stored JSON of any kind.
	Params     Params
	Raw        json.RawMessage
	ShowsUntil time.Time
	CreatedAt  time.Time
	EmailSent  *time.Time
	Dismissed  *time.Time
}

// Store is the account_notices table. The operator CLI uses the system pool; the API uses the request pool, where row-level security
// scopes every statement to the signed-in account.
type Store struct{ DB *sql.DB }

// Ensure creates the account's in-app notice with the given params if it has none; an existing row (and its dates) is left alone.
// created says whether this call made it.
func (s Store) Ensure(ctx context.Context, uid uuid.UUID, kind string, p Params) (created bool, err error) {
	until, err := p.ShowsUntil()
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO account_notices (user_id, kind, params, shows_until) VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, kind) DO NOTHING`,
		uid, kind, raw, until)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClaimEmail takes the right to send the account's email: it succeeds only when the email has not been sent and no other run holds a
// fresh claim. It returns the params stored on the row, which are the dates the email must state.
func (s Store) ClaimEmail(ctx context.Context, uid uuid.UUID, kind string) (Params, bool, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `
		UPDATE account_notices SET email_claimed_at = now()
		 WHERE user_id = $1 AND kind = $2 AND email_sent_at IS NULL
		   AND (email_claimed_at IS NULL OR email_claimed_at < now() - make_interval(secs => $3))
		RETURNING params`, uid, kind, ClaimStale.Seconds()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Params{}, false, nil
	}
	if err != nil {
		return Params{}, false, err
	}
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return Params{}, false, err
	}
	return p, true, nil
}

// MarkSent records that the email went out: the evidence that notice was given.
func (s Store) MarkSent(ctx context.Context, uid uuid.UUID, kind string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE account_notices SET email_sent_at = now() WHERE user_id = $1 AND kind = $2 AND email_sent_at IS NULL`, uid, kind)
	return err
}

// ReleaseClaim gives the claim back after a failed send, so a later run retries it.
func (s Store) ReleaseClaim(ctx context.Context, uid uuid.UUID, kind string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE account_notices SET email_claimed_at = NULL WHERE user_id = $1 AND kind = $2 AND email_sent_at IS NULL`, uid, kind)
	return err
}

// List is the account's notices that are still within their window, newest first (the caller's account only, by row-level security). The
// transactional billing emails (kinds billing_*) are email-only: they have rows here for their claim, and are never listed.
func (s Store) List(ctx context.Context, uid uuid.UUID, now time.Time) ([]Notice, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT kind, params, shows_until, created_at, email_sent_at, dismissed_at FROM account_notices
		 WHERE user_id = $1 AND shows_until > $2 AND kind NOT LIKE 'billing\_%' ORDER BY created_at DESC`, uid, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notice
	for rows.Next() {
		var n Notice
		var raw []byte
		var sent, dis sql.NullTime
		if err := rows.Scan(&n.Kind, &raw, &n.ShowsUntil, &n.CreatedAt, &sent, &dis); err != nil {
			return nil, err
		}
		n.Raw = raw
		_ = json.Unmarshal(raw, &n.Params) // other kinds carry other fields; the launch notice's dates are empty for them
		if sent.Valid {
			n.EmailSent = &sent.Time
		}
		if dis.Valid {
			n.Dismissed = &dis.Time
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Dismiss hides the notice's banner for the account (the Settings row stays). It is idempotent; false means the account has no such notice.
func (s Store) Dismiss(ctx context.Context, uid uuid.UUID, kind string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE account_notices SET dismissed_at = COALESCE(dismissed_at, now()) WHERE user_id = $1 AND kind = $2`, uid, kind)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// State is what the operator's dry run needs about one account's row.
type State struct {
	Exists    bool
	EmailSent bool
	Params    Params
	CreatedAt time.Time
}

// States returns the state of every account's row of this kind (operator side: the system pool).
func (s Store) States(ctx context.Context, kind string) (map[uuid.UUID]State, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT user_id, params, email_sent_at IS NOT NULL, created_at FROM account_notices WHERE kind = $1`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]State{}
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		st := State{Exists: true}
		if err := rows.Scan(&id, &raw, &st.EmailSent, &st.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &st.Params); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// EnsureAny is Ensure for any kind: params is whatever JSON the kind states, shownUntil when its in-app notice stops showing.
func (s Store) EnsureAny(ctx context.Context, uid uuid.UUID, kind string, params any, shownUntil time.Time) (created bool, err error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO account_notices (user_id, kind, params, shows_until) VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, kind) DO NOTHING`,
		uid, kind, raw, shownUntil)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClaimEmailAny is ClaimEmail for any kind; it returns the stored params as JSON.
func (s Store) ClaimEmailAny(ctx context.Context, uid uuid.UUID, kind string) (json.RawMessage, bool, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `
		UPDATE account_notices SET email_claimed_at = now()
		 WHERE user_id = $1 AND kind = $2 AND email_sent_at IS NULL
		   AND (email_claimed_at IS NULL OR email_claimed_at < now() - make_interval(secs => $3))
		RETURNING params`, uid, kind, ClaimStale.Seconds()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}
