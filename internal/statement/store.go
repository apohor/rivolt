package statement

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// RetryWindow is how long a failed send may be retried before the
// period is given up on, and MaxAttempts caps the retries within it.
// (The send window itself is the user's local days 1-2, so in practice
// it is the shorter bound.)
const (
	RetryWindow = 3 * 24 * time.Hour
	MaxAttempts = 3
)

// SendRetention is how long statement_email_sends rows are kept.
const SendRetention = 13 * 31 * 24 * time.Hour

// Prefs is one user's opt-in.
type Prefs struct {
	Enabled  bool
	Timezone string // IANA; "UTC" when unset
}

// Candidate is an opted-in user the job may send to.
type Candidate struct {
	UserID    uuid.UUID
	Timezone  string
	Email     string
	FirstName string
	// HasApp: the account has a live iPhone-app session, so the app link is left out.
	HasApp bool
}

// Store is the persistence the job and the API need. The Postgres
// implementation is pgStore; tests use an in-memory fake.
type Store interface {
	GetPrefs(ctx context.Context, uid uuid.UUID) (Prefs, error)
	SetPrefs(ctx context.Context, uid uuid.UUID, p Prefs) error
	// LastSentPeriod is the newest period with status 'sent', or "".
	LastSentPeriod(ctx context.Context, uid uuid.UUID) (string, error)
	// Email returns the account's VERIFIED email address ("" = none or
	// unverified): statements are only ever mailed to an address the
	// identity provider verified.
	Email(ctx context.Context, uid uuid.UUID) (string, error)
	// OptedIn lists users with enabled=true and an email address.
	OptedIn(ctx context.Context) ([]Candidate, error)
	// Claim atomically reserves (user, period). It returns true for a
	// new row or a 'failed' row inside RetryWindow; a period that is
	// sending, sent or skipped (or a failed one past the window) is not
	// claimable. This is the idempotency key: it holds across replicas.
	Claim(ctx context.Context, uid uuid.UUID, period string, now time.Time) (bool, error)
	Mark(ctx context.Context, uid uuid.UUID, period, status, reason string, now time.Time) error
	// StuckSending counts claims left in 'sending' for longer than d
	// (a crash between Claim and Mark: at-most-once, never resent, so
	// this is how a silently lost month becomes visible).
	StuckSending(ctx context.Context, d time.Duration, now time.Time) (int, error)
	// Purge deletes send rows created before cutoff.
	Purge(ctx context.Context, cutoff time.Time) (int64, error)
}

type pgStore struct{ db *sql.DB }

// NewStore returns the Postgres store.
func NewStore(db *sql.DB) Store { return &pgStore{db: db} }

func (s *pgStore) GetPrefs(ctx context.Context, uid uuid.UUID) (Prefs, error) {
	var p Prefs
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled, timezone FROM statement_email_prefs WHERE user_id = $1`, uid).Scan(&p.Enabled, &p.Timezone)
	if errors.Is(err, sql.ErrNoRows) {
		return Prefs{Timezone: "UTC"}, nil
	}
	return p, err
}

func (s *pgStore) SetPrefs(ctx context.Context, uid uuid.UUID, p Prefs) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO statement_email_prefs (user_id, enabled, timezone, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, timezone = EXCLUDED.timezone, updated_at = NOW()`,
		uid, p.Enabled, p.Timezone)
	return err
}

func (s *pgStore) LastSentPeriod(ctx context.Context, uid uuid.UUID) (string, error) {
	var p sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT period FROM statement_email_sends
		 WHERE user_id = $1 AND status = 'sent' ORDER BY period DESC LIMIT 1`, uid).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return p.String, err
}

func (s *pgStore) Email(ctx context.Context, uid uuid.UUID) (string, error) {
	var e sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = $1 AND email_verified`, uid).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return e.String, err
}

func (s *pgStore) OptedIn(ctx context.Context) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.user_id, p.timezone, u.email, COALESCE(u.display_name, ''),
		       EXISTS (SELECT 1 FROM sessions s WHERE s.user_id = u.id AND s.kind = 'app' AND s.revoked_at IS NULL AND s.expires_at > now())
		  FROM statement_email_prefs p
		  JOIN users u ON u.id = p.user_id
		 WHERE p.enabled AND COALESCE(u.email, '') <> '' AND u.email_verified AND NOT u.email_undeliverable AND COALESCE(u.disabled, FALSE) = FALSE
		 ORDER BY p.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.UserID, &c.Timezone, &c.Email, &c.FirstName, &c.HasApp); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *pgStore) Claim(ctx context.Context, uid uuid.UUID, period string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO statement_email_sends (user_id, period, status, attempts, created_at)
		VALUES ($1, $2, 'sending', 1, $3::timestamptz)
		ON CONFLICT (user_id, period) DO UPDATE
		   SET status = 'sending', attempts = statement_email_sends.attempts + 1
		 WHERE statement_email_sends.status = 'failed'
		   AND statement_email_sends.attempts < $5
		   AND statement_email_sends.created_at > $3::timestamptz - make_interval(secs => $4)`,
		uid, period, now.UTC(), RetryWindow.Seconds(), MaxAttempts)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *pgStore) Mark(ctx context.Context, uid uuid.UUID, period, status, reason string, now time.Time) error {
	var sentAt any
	if status == "sent" {
		sentAt = now.UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE statement_email_sends
		   SET status = $3, reason = NULLIF($4, ''), sent_at = COALESCE($5::timestamptz, sent_at)
		 WHERE user_id = $1 AND period = $2`, uid, period, status, reason, sentAt)
	return err
}

func (s *pgStore) StuckSending(ctx context.Context, d time.Duration, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM statement_email_sends
		 WHERE status = 'sending' AND created_at < $1::timestamptz - make_interval(secs => $2)`,
		now.UTC(), d.Seconds()).Scan(&n)
	return n, err
}

func (s *pgStore) Purge(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM statement_email_sends WHERE created_at < $1::timestamptz`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
