package secrets

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivianexpiry"
)

// RivianSessionLister lists every stored Rivian session for rivianexpiry. It decrypts each
// (the sign-in time is in the sealed session) and returns only the session and the
// user's needs_reauth flag.
type RivianSessionLister struct {
	db    *sql.DB
	store *Store
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
	for _, r := range ids {
		// Every stored session passes through here once an hour: drop the legacy access and
		// refresh tokens an older version kept in it (best effort, never changes the version).
		_, _ = ScrubLegacyRivianTokens(ctx, l.store, r.id)
		sess, err := LoadRivianSession(ctx, l.store, r.id)
		if err != nil || sess.UserSessionToken == "" {
			continue // an unreadable or empty session is not ours to warn about
		}
		out = append(out, rivianexpiry.Entry{User: r.id, NeedsReauth: r.needs, OptedOut: r.off, Session: sess})
	}
	return out, nil
}
