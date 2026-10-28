package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivianexpiry"
)

// RivianSessionLister lists every stored Rivian session for rivianexpiry. It decrypts each
// (the sign-in time is in the sealed session) and returns only the session and the
// user's needs_reauth flag.
type RivianSessionLister struct {
	db    *sql.DB
	store *Store
	// Log, when set, gets one line per listing with the number of stored sessions the legacy-token
	// scrub changed (a count only). The scrub is temporary: once prod logs 0 for a day it, and
	// this decryption of every session just to read the sign-in time, can go.
	Log *slog.Logger
}

// NewRivianSessionLister returns the lister over the system pool and the store.
func NewRivianSessionLister(pool *sql.DB, store *Store) *RivianSessionLister {
	return &RivianSessionLister{db: pool, store: store}
}

// List implements rivianexpiry.Sessions.
func (l *RivianSessionLister) List(ctx context.Context) ([]rivianexpiry.Entry, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT us.user_id, COALESCE(u.needs_reauth, FALSE), u.rivian_expiry_emails_off_at IS NOT NULL
		FROM user_secrets us JOIN users u ON u.id = us.user_id
		WHERE us.name = $1`, rivianSessionName)
	if err != nil {
		return nil, fmt.Errorf("list rivian sessions: %w", err)
	}
	type row struct {
		id    uuid.UUID
		needs bool
		off   bool
	}
	var ids []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.needs, &r.off); err != nil {
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
		// Every stored session passes through here once an hour: drop the legacy access and
		// refresh tokens an older version kept in it (best effort, never changes the version).
		if ok, _ := ScrubLegacyRivianTokens(ctx, l.store, r.id); ok {
			scrubbed++
		}
		sess, err := LoadRivianSession(ctx, l.store, r.id)
		if err != nil || sess.UserSessionToken == "" {
			continue // an unreadable or empty session is not ours to warn about
		}
		out = append(out, rivianexpiry.Entry{User: r.id, NeedsReauth: r.needs, OptedOut: r.off, Session: sess})
	}
	if l.Log != nil {
		l.Log.Info("rivian session scrub", "sessions", len(ids), "scrubbed", scrubbed)
	}
	return out, nil
}

// WithLog makes the lister log the scrub count of each pass.
func (l *RivianSessionLister) WithLog(log *slog.Logger) *RivianSessionLister {
	l.Log = log
	return l
}
