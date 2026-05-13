package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AdminUserRow is the projection /api/admin/users serves. The
// activity counters (vehicles / drives / imports / rivian_connected
// / last_seen_at) let the admin see "did this user actually do
// anything?" without a per-user drill-down. Cheap scalar subqueries
// at our scale (<100 users); paginate if that ever changes.
type AdminUserRow struct {
	ID              uuid.UUID  `json:"id"`
	Username        string     `json:"username"`
	Email           string     `json:"email,omitempty"`
	DisplayName     string     `json:"display_name,omitempty"`
	Role            string     `json:"role"`
	Disabled        bool       `json:"disabled"`
	CreatedAt       time.Time  `json:"created_at"`
	VehicleCount    int        `json:"vehicle_count"`
	DriveCount      int        `json:"drive_count"`
	ImportCount     int        `json:"import_count"`
	RivianConnected bool       `json:"rivian_connected"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
}

// ListUsersForAdmin returns every user row, role-stamped and
// ordered by created_at so the admin page is stable across
// reloads. No paging — a self-hosted install will not have
// enough users to need it. If that ever stops being true,
// add LIMIT/OFFSET parameters.
func ListUsersForAdmin(ctx context.Context, d *sql.DB) ([]AdminUserRow, error) {
	if d == nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT u.id, u.username, COALESCE(u.email, ''), COALESCE(u.display_name, ''),
		       u.role, u.disabled, u.created_at,
		       (SELECT COUNT(*) FROM vehicles WHERE user_id = u.id),
		       (SELECT COUNT(*) FROM drives   WHERE user_id = u.id),
		       (SELECT COUNT(*) FROM imports  WHERE user_id = u.id),
		       EXISTS(SELECT 1 FROM user_secrets
		               WHERE user_id = u.id AND name = 'rivian.session'),
		       (SELECT MAX(last_seen_at) FROM sessions WHERE user_id = u.id)
		FROM users u
		ORDER BY u.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list users for admin: %w", err)
	}
	defer rows.Close()
	var out []AdminUserRow
	for rows.Next() {
		var r AdminUserRow
		var lastSeen sql.NullTime
		if err := rows.Scan(
			&r.ID, &r.Username, &r.Email, &r.DisplayName, &r.Role, &r.Disabled, &r.CreatedAt,
			&r.VehicleCount, &r.DriveCount, &r.ImportCount, &r.RivianConnected, &lastSeen,
		); err != nil {
			return nil, fmt.Errorf("list users for admin scan: %w", err)
		}
		if lastSeen.Valid {
			t := lastSeen.Time
			r.LastSeenAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteUser removes a user row. ON DELETE CASCADE on every
// dependent table (vehicles, drives, charges, samples,
// user_settings, user_secrets, sessions, push_subscriptions, …)
// means this is sufficient to fully evict the tenant; the admin
// endpoint relies on that contract instead of a hand-rolled
// transactional sweep.
func DeleteUser(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	if d == nil || uid == uuid.Nil {
		return nil
	}
	_, err := d.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, uid)
	return err
}

// ListUsersWithRivianSession returns every user_id that currently has
// a persisted rivian.session blob in user_secrets. The boot path
// uses this to hydrate every user's *LiveClient before the
// StateMonitor goroutines start, so a pod restart is invisible to
// the data plane regardless of which users are active.
//
// The query reads ciphertext-presence only — it does NOT decrypt
// the blob, so it's safe to run before the secret-sealer is fully
// wired (e.g. during a future rotation window).
func ListUsersWithRivianSession(ctx context.Context, db *sql.DB) ([]uuid.UUID, error) {
	if db == nil {
		return nil, nil
	}
	const q = `SELECT user_id FROM user_secrets WHERE name = 'rivian.session'`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list users with rivian session: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list users with rivian session scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// VehicleOwner resolves an internal vehicles.id (UUID) back to its
// owning user_id. Used by the StateMonitor recorder to attribute
// frames to the right user even when a single pod monitors vehicles
// belonging to many users (post-PR-6 lease coordinator).
//
// Returns (uuid.Nil, nil) when no row exists; callers should treat
// that as "stale subscription, drop the frame" rather than an error.
func VehicleOwner(ctx context.Context, db *sql.DB, vehicleID uuid.UUID) (uuid.UUID, error) {
	if db == nil || vehicleID == uuid.Nil {
		return uuid.Nil, nil
	}
	var owner uuid.UUID
	err := db.QueryRowContext(ctx,
		`SELECT user_id FROM vehicles WHERE id = $1`, vehicleID).Scan(&owner)
	if err == sql.ErrNoRows {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("vehicle owner: %w", err)
	}
	return owner, nil
}

// VehicleOwnerByRivianID is the Rivian-gateway-id-keyed sibling of
// VehicleOwner. The lease coordinator queries this when it acquires
// a lease so the per-user *LiveClient can be hydrated before the WS
// subscription starts.
func VehicleOwnerByRivianID(ctx context.Context, db *sql.DB, rivianID string) (uuid.UUID, error) {
	if db == nil || rivianID == "" {
		return uuid.Nil, nil
	}
	var owner uuid.UUID
	err := db.QueryRowContext(ctx,
		`SELECT user_id FROM vehicles WHERE rivian_vehicle_id = $1 LIMIT 1`,
		rivianID).Scan(&owner)
	if err == sql.ErrNoRows {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("vehicle owner by rivian id: %w", err)
	}
	return owner, nil
}

// ListVehiclesWithOwners returns every (user_id, rivian_vehicle_id)
// tuple in the vehicles table. Used by the lease coordinator's
// vehicle source so the cluster's reconciliation set is keyed by
// user, not by rivian id alone — two users with the same rivian id
// (yes, this is possible: shared family account, dev/prod accidental
// reuse) are distinct lease subjects.
type VehicleOwnership struct {
	UserID    uuid.UUID
	VehicleID string // rivian gateway id
}

// ListVehiclesWithOwners returns every (user_id, rivian_vehicle_id)
// pair in the vehicles table. Empty rivian ids are skipped — the row
// is a placeholder waiting for first contact, not a subscription
// candidate.
func ListVehiclesWithOwners(ctx context.Context, db *sql.DB) ([]VehicleOwnership, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT user_id, rivian_vehicle_id
		FROM vehicles
		WHERE rivian_vehicle_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("list vehicles with owners: %w", err)
	}
	defer rows.Close()
	var out []VehicleOwnership
	for rows.Next() {
		var v VehicleOwnership
		if err := rows.Scan(&v.UserID, &v.VehicleID); err != nil {
			return nil, fmt.Errorf("list vehicles with owners scan: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
