package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// VehicleResolver translates Rivian gateway vehicle-id strings
// (e.g. "01-242521064") into the internal vehicles.id UUID, creating
// the row on first sight. Results are cached in-process so the hot
// path (per-sample writes) doesn't hit the DB on every call.
//
// Safe for concurrent use.
type VehicleResolver struct {
	db     *sql.DB
	userID uuid.UUID

	mu    sync.RWMutex
	cache map[string]uuid.UUID // rivianVehicleID -> internal UUID
}

// NewVehicleResolver builds a resolver scoped to a single user.
func NewVehicleResolver(d *sql.DB, userID uuid.UUID) *VehicleResolver {
	return &VehicleResolver{
		db:     d,
		userID: userID,
		cache:  make(map[string]uuid.UUID),
	}
}

// Resolve returns the internal UUID for a Rivian vehicle-id string,
// upserting a bare vehicles row (user_id + rivian_vehicle_id) on
// first sight. Empty rivianID is an error — the stores should never
// be asked to write a row with no vehicle association.
func (r *VehicleResolver) Resolve(ctx context.Context, rivianID string) (uuid.UUID, error) {
	if rivianID == "" {
		return uuid.Nil, fmt.Errorf("vehicles: rivian id is empty")
	}
	r.mu.RLock()
	id, ok := r.cache[rivianID]
	r.mu.RUnlock()
	if ok {
		return id, nil
	}
	// Upsert and read back. ON CONFLICT DO UPDATE ... RETURNING id
	// gives us a one-round-trip path whether the row is new or
	// already exists, since DO NOTHING's RETURNING would be empty
	// on the conflict case.
	var got uuid.UUID
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO vehicles (user_id, rivian_vehicle_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, rivian_vehicle_id) DO UPDATE
			SET updated_at = vehicles.updated_at
		RETURNING id
	`, r.userID, rivianID).Scan(&got)
	if err != nil {
		return uuid.Nil, fmt.Errorf("vehicles resolve %q: %w", rivianID, err)
	}
	r.mu.Lock()
	r.cache[rivianID] = got
	r.mu.Unlock()
	return got, nil
}

// RivianID returns the Rivian gateway string for an internal UUID.
// Used by readers that need to present vehicle_id in the API shape
// the UI expects. Uncached — read-path uses are not hot enough to
// justify an inverse cache.
func (r *VehicleResolver) RivianID(ctx context.Context, id uuid.UUID) (string, error) {
	var s string
	err := r.db.QueryRowContext(ctx,
		`SELECT rivian_vehicle_id FROM vehicles WHERE id = $1`, id).Scan(&s)
	return s, err
}
