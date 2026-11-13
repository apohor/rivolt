package tripshare

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// MaxActivePerUser caps the live links an account keeps.
	MaxActivePerUser = 25
	// MaxCreatesPerHour caps how many links an account makes in an hour, counted from the table so every replica agrees.
	MaxCreatesPerHour = 20
	// KeepEndedFor is how long an expired or revoked row stays before the cleanup removes it.
	KeepEndedFor = 30 * 24 * time.Hour
)

var (
	ErrNotFound = errors.New("tripshare: not found")
	ErrCap      = errors.New("tripshare: too many active links")
	ErrRate     = errors.New("tripshare: too many links created")
)

// Link is an owner's view of one link: never the token (shown once at creation) and never the snapshot.
type Link struct {
	ID          uuid.UUID  `json:"id"`
	SavedTripID uuid.UUID  `json:"saved_trip_id"`
	Title       string     `json:"title"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

// Store reads and writes trip_shares. Req is the request pool (tenant row-level security applies: the account's own rows only), Sys the
// system pool, used for the public lookup, which has no account, and for the cleanup.
type Store struct {
	Req, Sys *sql.DB
}

// Create stores a link for uid and returns it, or ErrCap / ErrRate. The two limits are counted in the same transaction as the insert
// under a per-account advisory lock, so concurrent creates cannot both slip past a limit.
func (s *Store) Create(ctx context.Context, uid, savedTripID uuid.UUID, title string, hash []byte, cutM float64, snapshot []byte, expiresAt *time.Time) (Link, error) {
	tx, err := s.Req.BeginTx(ctx, nil)
	if err != nil {
		return Link{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('trip_share:' || $1::text, 0))`, uid); err != nil {
		return Link{}, err
	}
	var active, recent int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())),
		       count(*) FILTER (WHERE created_at > now() - interval '1 hour')
		FROM trip_shares WHERE user_id = $1`, uid).Scan(&active, &recent); err != nil {
		return Link{}, err
	}
	if recent >= MaxCreatesPerHour {
		return Link{}, ErrRate
	}
	if active >= MaxActivePerUser {
		return Link{}, ErrCap
	}
	var l Link
	l.SavedTripID, l.Title, l.ExpiresAt = savedTripID, title, expiresAt
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO trip_shares (user_id, saved_trip_id, token_hash, title, cut_radius_m, snapshot, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`, uid, savedTripID, hash, title, int(cutM), snapshot, expiresAt).Scan(&l.ID, &l.CreatedAt); err != nil {
		return Link{}, err
	}
	return l, tx.Commit()
}

// List is uid's links for one saved trip, newest first (ended ones included, so the owner sees what was revoked).
func (s *Store) List(ctx context.Context, uid, savedTripID uuid.UUID) ([]Link, error) {
	rows, err := s.Req.QueryContext(ctx, `
		SELECT id, saved_trip_id, title, created_at, expires_at, revoked_at
		FROM trip_shares WHERE user_id = $1 AND saved_trip_id = $2
		ORDER BY created_at DESC LIMIT 100`, uid, savedTripID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.SavedTripID, &l.Title, &l.CreatedAt, &l.ExpiresAt, &l.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Revoke ends a link; repeating it is a no-op. ErrNotFound for a link that is not uid's.
func (s *Store) Revoke(ctx context.Context, uid, id uuid.UUID) error {
	res, err := s.Req.ExecContext(ctx, `UPDATE trip_shares SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1 AND user_id = $2`, id, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Lookup is the public read: the snapshot of a live link by its token hash, or ErrNotFound for unknown, expired and revoked alike.
// It calls the SECURITY DEFINER function on the system pool (the request role cannot execute it).
func (s *Store) Lookup(ctx context.Context, hash []byte) ([]byte, error) {
	var snap []byte
	if err := s.Sys.QueryRowContext(ctx, `SELECT trip_share_get($1)`, hash).Scan(&snap); err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, ErrNotFound
	}
	return snap, nil
}

// Sweep deletes rows that ended (expired or revoked) more than KeepEndedFor ago. System pool; idempotent, so every replica may run it.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	res, err := s.Sys.ExecContext(ctx, `
		DELETE FROM trip_shares
		WHERE LEAST(COALESCE(revoked_at, 'infinity'), COALESCE(expires_at, 'infinity')) < now() - make_interval(secs => $1)`, KeepEndedFor.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MetricEvents are the values of rivolt_trip_share_total{event}.
var MetricEvents = []string{"created", "revoked", "viewed", "miss", "rate_limited", "busy", "refused"}
