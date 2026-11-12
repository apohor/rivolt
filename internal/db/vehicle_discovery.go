package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
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

// Discovery states kept in vehicle_discovery (migration 0109). No row is "looking": nobody has asked Rivian yet.
const (
	DiscoveryNone  = "none"  // Rivian answered and the account has no vehicle yet
	DiscoveryError = "error" // the lookup failed; it is tried again after next_check_at
)

// RecordDiscovery stores what the owner's lookup found for a user, to be looked at again at next (a system-pool write by the owner).
func RecordDiscovery(ctx context.Context, d *sql.DB, uid uuid.UUID, state string, next time.Time) error {
	// An owner-side write: with the group's fence token in ctx it is refused (fence.ErrFenced) once the pod no longer holds the group.
	_, err := fence.Exec(ctx, d, `
		INSERT INTO vehicle_discovery (user_id, state, checked_at, next_check_at) SELECT $1::uuid, $2::text, now(), $3::timestamptz WHERE `+fence.OK+`
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, checked_at = now(), next_check_at = EXCLUDED.next_check_at`, uid, state, next)
	return err
}

// ClearDiscovery removes a user's row: their vehicles were stored, so there is nothing left to wait for.
func ClearDiscovery(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	_, err := fence.Exec(ctx, d, `DELETE FROM vehicle_discovery WHERE user_id = $1 AND `+fence.OK, uid)
	return err
}

// GetDiscovery returns the user's stored state, or ok false when there is none ("looking"). On the request pool it reads the caller's own row.
func GetDiscovery(ctx context.Context, d *sql.DB, uid uuid.UUID) (state string, ok bool, err error) {
	err = d.QueryRowContext(ctx, `SELECT state FROM vehicle_discovery WHERE user_id = $1`, uid).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return state, err == nil, err
}

// ListUnlinkedWithDiscoveryAmong returns, of among (the users of the groups this pod owns), the ones that still have a discovery row but no stored
// Rivian session: they signed out, so the row's answer ("none", say) belongs to an account they no longer use.
func ListUnlinkedWithDiscoveryAmong(ctx context.Context, d *sql.DB, among []uuid.UUID) ([]uuid.UUID, error) {
	if d == nil || len(among) == 0 {
		return nil, nil
	}
	ids := make([]string, len(among))
	for i, u := range among {
		ids[i] = u.String()
	}
	rows, err := d.QueryContext(ctx, `
		SELECT vd.user_id FROM vehicle_discovery vd
		 WHERE vd.user_id = ANY($1::uuid[])
		   AND NOT EXISTS (SELECT 1 FROM user_secrets s WHERE s.user_id = vd.user_id AND s.name = 'rivian.session')`, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("list unlinked users with a discovery row: %w", err)
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

// ListLinkedUsersWithoutVehiclesAmong is ListLinkedUsersWithoutVehicles restricted to among (the users of the groups this pod owns) and to the
// ones due: no stored discovery row, or one whose next_check_at has passed. Oldest first; at most limit.
func ListLinkedUsersWithoutVehiclesAmong(ctx context.Context, d *sql.DB, among []uuid.UUID, limit int) ([]uuid.UUID, error) {
	if d == nil || len(among) == 0 {
		return nil, nil
	}
	ids := make([]string, len(among))
	for i, u := range among {
		ids[i] = u.String()
	}
	rows, err := d.QueryContext(ctx, `
		SELECT u.id FROM users u
		 WHERE u.id = ANY($1::uuid[])
		   AND EXISTS (SELECT 1 FROM user_secrets s WHERE s.user_id = u.id AND s.name = 'rivian.session')
		   AND NOT u.needs_reauth
		   AND NOT EXISTS (SELECT 1 FROM vehicles v
		                    WHERE v.user_id = u.id AND v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%')
		   AND NOT EXISTS (SELECT 1 FROM vehicle_discovery vd WHERE vd.user_id = u.id AND vd.next_check_at > now())
		 ORDER BY u.created_at, u.id
		 LIMIT $2`, "{"+strings.Join(ids, ",")+"}", limit)
	if err != nil {
		return nil, fmt.Errorf("list linked users without vehicles among: %w", err)
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

// ListLinkedUsersWithVehiclesAmong returns, of among (the users of the groups this pod owns), the ones with a stored Rivian session, not flagged for
// sign-in again, and at least one real vehicle row: the users whose account the owner refreshes on a slow cadence, so a second truck added to the
// Rivian account is picked up without a request-path Rivian call.
func ListLinkedUsersWithVehiclesAmong(ctx context.Context, d *sql.DB, among []uuid.UUID) ([]uuid.UUID, error) {
	if d == nil || len(among) == 0 {
		return nil, nil
	}
	ids := make([]string, len(among))
	for i, u := range among {
		ids[i] = u.String()
	}
	rows, err := d.QueryContext(ctx, `
		SELECT u.id FROM users u
		 WHERE u.id = ANY($1::uuid[])
		   AND EXISTS (SELECT 1 FROM user_secrets s WHERE s.user_id = u.id AND s.name = 'rivian.session')
		   AND NOT u.needs_reauth
		   AND EXISTS (SELECT 1 FROM vehicles v
		                WHERE v.user_id = u.id AND v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%')
		 ORDER BY u.created_at, u.id`, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return nil, fmt.Errorf("list linked users with vehicles among: %w", err)
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
