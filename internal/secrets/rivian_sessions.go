package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivianexpiry"
)

// RivianSessionLister lists every stored Rivian session for rivianexpiry. It reads the sign-in
// time from users.rivian_signed_in_at and never opens a session for it. The one exception is a
// session whose time was never recorded (NULL: made before the column existed): it is opened
// once to fill the column, and not again.
type RivianSessionLister struct {
	db    *sql.DB
	store *Store
	// Log, when set, gets one line per listing with the number of stored sessions the legacy-token
	// scrub changed (a count only). The scrub is temporary: once prod logs 0 for a day it, and
	// the decryption of every session it needs, go.
	Log *slog.Logger
	// scrub turns the legacy-token scrub on (WithScrub). It opens every session on each pass, so it is
	// off unless asked for.
	scrub bool
}

// NewRivianSessionLister returns the lister over the system pool and the store.
func NewRivianSessionLister(pool *sql.DB, store *Store) *RivianSessionLister {
	return &RivianSessionLister{db: pool, store: store}
}

// List implements rivianexpiry.Sessions.
func (l *RivianSessionLister) List(ctx context.Context) ([]rivianexpiry.Entry, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT us.user_id, COALESCE(u.needs_reauth, FALSE), u.rivian_expiry_emails_off_at IS NOT NULL, u.rivian_signed_in_at
		FROM user_secrets us JOIN users u ON u.id = us.user_id
		WHERE us.name = $1`, rivianSessionName)
	if err != nil {
		return nil, fmt.Errorf("list rivian sessions: %w", err)
	}
	type row struct {
		id       uuid.UUID
		needs    bool
		off      bool
		signedIn sql.NullTime
	}
	var ids []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.needs, &r.off, &r.signedIn); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list rivian sessions: %w", err)
		}
		ids = append(ids, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	out := make([]rivianexpiry.Entry, 0, len(ids))
	scrubbed := 0
	for _, r := range ids {
		// Temporary: every stored session passes through the legacy-token scrub once an hour (best
		// effort, never changes the version). It goes with the scrub.
		if l.scrub {
			if ok, _ := ScrubLegacyRivianTokens(ctx, l.store, r.id); ok {
				scrubbed++
			}
		}
		at := r.signedIn.Time
		if !r.signedIn.Valid {
			// Not recorded yet: open this session once and write the time down.
			at = l.backfill(ctx, r.id)
		}
		if !at.IsZero() && at.Equal(RivianSignedInUnknown) {
			at = time.Time{}
		}
		out = append(out, rivianexpiry.Entry{User: r.id, NeedsReauth: r.needs, OptedOut: r.off, SignedIn: at})
	}
	if l.Log != nil && l.scrub {
		l.Log.Info("rivian session scrub", "sessions", len(ids), "scrubbed", scrubbed)
	}
	return out, nil
}

// backfill reads a session's sign-in time once and records it, only if nobody recorded one
// meanwhile (a sign-in writes the column itself). It returns the time, zero when unknown or when
// the session cannot be read (then nothing is written and a later pass tries again).
func (l *RivianSessionLister) backfill(ctx context.Context, uid uuid.UUID) time.Time {
	sess, err := LoadRivianSession(ctx, l.store, uid)
	if err != nil || sess.UserSessionToken == "" {
		return time.Time{}
	}
	at := sess.AuthenticatedAt
	rec := at
	if rec.IsZero() {
		rec = RivianSignedInUnknown
	}
	if _, err := l.db.ExecContext(ctx,
		`UPDATE users SET rivian_signed_in_at = $2 WHERE id = $1 AND rivian_signed_in_at IS NULL`, uid, rec.UTC()); err != nil && l.Log != nil {
		l.Log.Warn("rivian sign-in time backfill failed", "err", err.Error())
	}
	return at
}

// WithScrub turns the temporary legacy-token scrub on. Without it a pass opens a session only to
// backfill a sign-in time that was never recorded.
func (l *RivianSessionLister) WithScrub() *RivianSessionLister {
	l.scrub = true
	return l
}

// WithLog makes the lister log the scrub count of each pass.
func (l *RivianSessionLister) WithLog(log *slog.Logger) *RivianSessionLister {
	l.Log = log
	return l
}
