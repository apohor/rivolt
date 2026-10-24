package weekly

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

type pgSource struct {
	db   *sql.DB
	skip uuid.UUID
}

// NewSource reads from Postgres. skip (the demo account) is never a candidate.
func NewSource(db *sql.DB, skip uuid.UUID) Source { return &pgSource{db: db, skip: skip} }

func (s *pgSource) Candidates(ctx context.Context) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.user_id, u.email,
		       COALESCE(NULLIF(p.timezone, ''), (SELECT sp.timezone FROM statement_email_prefs sp WHERE sp.user_id = p.user_id), 'UTC'),
		       COALESCE((SELECT v.model FROM vehicles v
		                  WHERE v.user_id = p.user_id AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'
		                  ORDER BY v.created_at LIMIT 1), '')
		  FROM engagement_prefs p
		  JOIN users u ON u.id = p.user_id
		 WHERE p.weekly_recap AND NOT u.disabled AND u.email_verified AND NOT u.email_undeliverable AND COALESCE(u.email, '') <> ''
		   AND p.user_id <> $1`, s.skip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.User, &c.Email, &c.Timezone, &c.Model); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *pgSource) Enabled(ctx context.Context, uid uuid.UUID) (bool, error) {
	var on bool
	err := s.db.QueryRowContext(ctx, `SELECT weekly_recap FROM engagement_prefs WHERE user_id = $1`, uid).Scan(&on)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return on, err
}
