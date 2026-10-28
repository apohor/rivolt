package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// RivianAccountSource reads a user's shared Rivian account state for rivian.AccountSyncer:
// the version of the stored session (its updated_at, so any pod's sign-in or sign-out
// changes it) with the needs_reauth flag. It reads the session with LoadRivianSession.
type RivianAccountSource struct {
	db    *sql.DB
	store *Store
}

// NewRivianAccountSource returns the source over pool (the system pool: it reads the users
// and user_secrets tables of every user) and store.
func NewRivianAccountSource(pool *sql.DB, store *Store) *RivianAccountSource {
	return &RivianAccountSource{db: pool, store: store}
}

// States implements rivian.AccountStateSource.
func (s *RivianAccountSource) States(ctx context.Context, uids []uuid.UUID) (map[uuid.UUID]rivian.AccountDBState, error) {
	out := make(map[uuid.UUID]rivian.AccountDBState, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	ids := make([]string, len(uids))
	for i, u := range uids {
		ids[i] = u.String()
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, COALESCE(u.needs_reauth, FALSE), COALESCE(u.needs_reauth_reason, ''), us.updated_at
		FROM users u
		LEFT JOIN user_secrets us ON us.user_id = u.id AND us.name = $2
		WHERE u.id = ANY($1::uuid[])`, "{"+strings.Join(ids, ",")+"}", rivianSessionName)
	if err != nil {
		return nil, fmt.Errorf("rivian account states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id      uuid.UUID
			st      rivian.AccountDBState
			updated sql.NullTime
		)
		if err := rows.Scan(&id, &st.NeedsReauth, &st.Reason, &updated); err != nil {
			return nil, fmt.Errorf("rivian account states: %w", err)
		}
		if updated.Valid {
			st.SessionVersion = updated.Time
		} else {
			st.SessionVersion = time.Time{}
		}
		out[id] = st
	}
	return out, rows.Err()
}

// Session implements rivian.AccountStateSource.
func (s *RivianAccountSource) Session(ctx context.Context, uid uuid.UUID) (rivian.Session, error) {
	return LoadRivianSession(ctx, s.store, uid)
}
