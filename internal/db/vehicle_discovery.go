package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// ListLinkedUsersWithoutVehicles returns the users who have a stored Rivian session, are not flagged for sign-in again, and have no real vehicle row
// (the legacy electrafi rows are not real). Such a user has no group, so no lease and no owner: nothing records them until a lookup primes their
// vehicles (the sign-in's prime failed, or the account had none yet). Oldest first; at most limit.
func ListLinkedUsersWithoutVehicles(ctx context.Context, d *sql.DB, limit int) ([]uuid.UUID, error) {
	if d == nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT u.id FROM users u
		 WHERE EXISTS (SELECT 1 FROM user_secrets s WHERE s.user_id = u.id AND s.name = 'rivian.session')
		   AND NOT u.needs_reauth
		   AND NOT EXISTS (SELECT 1 FROM vehicles v
		                    WHERE v.user_id = u.id AND v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%')
		 ORDER BY u.created_at, u.id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list linked users without vehicles: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
