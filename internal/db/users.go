package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

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
