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

// OwnsRivianID reports whether the given user owns a vehicle
// registered under the given Rivian gateway vehicle-id.
//
// This is deliberately a plain SELECT (no upsert) so ownership
// probing can't be used to silently provision rows in another
// user's vehicles set — the write path goes through
// VehicleResolver.Resolve, which is user-scoped by construction.
//
// Used by the HTTP ownership middleware as the single seam that
// decides whether /api/state/{vehicleID} and friends are allowed
// to touch Rivian upstream on behalf of the session user. False
// with nil error means "not yours" and the caller must return 404
// (not 403) so enumerating vehicle-ids doesn't leak existence.
func OwnsRivianID(ctx context.Context, d *sql.DB, userID uuid.UUID, rivianID string) (bool, error) {
	if rivianID == "" || userID == uuid.Nil {
		return false, nil
	}
	var one int
	err := d.QueryRowContext(ctx, `
		SELECT 1 FROM vehicles
		WHERE user_id = $1 AND rivian_vehicle_id = $2
		LIMIT 1
	`, userID, rivianID).Scan(&one)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("vehicles ownership check: %w", err)
	}
	return true, nil
}

// VehicleSummary is the per-user vehicle metadata exposed to
// /api/vehicles/owned (the import-picker source). It deliberately
// omits operational fields (created_at, updated_at) the SPA doesn't
// need — keeping the wire shape narrow makes the picker dropdown
// trivially serialisable.
type VehicleSummary struct {
	ID              uuid.UUID `json:"id"`
	RivianVehicleID string    `json:"rivian_vehicle_id"`
	VIN             string    `json:"vin,omitempty"`
	DisplayName     string    `json:"display_name,omitempty"`
	Model           string    `json:"model,omitempty"`
	ModelYear       int       `json:"model_year,omitempty"`
	PackKWh         float64   `json:"pack_kwh,omitempty"`
}

// ListUserVehicles returns every real vehicle row owned by userID,
// excluding the legacy `electrafi-<hash>` synthetic rows that the
// pre-v0.17.21 importer used to create. Synthetic rows linger in
// existing installs because their drives/charges/samples still
// reference them; we hide them from the picker so a fresh import
// can only land on a Rivian-linked vehicle.
func ListUserVehicles(ctx context.Context, d *sql.DB, userID uuid.UUID) ([]VehicleSummary, error) {
	if d == nil || userID == uuid.Nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id,
		       COALESCE(rivian_vehicle_id, ''),
		       COALESCE(vin, ''),
		       COALESCE(display_name, ''),
		       COALESCE(model, ''),
		       COALESCE(model_year, 0),
		       COALESCE(pack_kwh, 0)
		  FROM vehicles
		 WHERE user_id = $1
		   AND rivian_vehicle_id NOT LIKE 'electrafi-%'
		 ORDER BY display_name NULLS LAST, rivian_vehicle_id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user vehicles: %w", err)
	}
	defer rows.Close()
	var out []VehicleSummary
	for rows.Next() {
		var v VehicleSummary
		if err := rows.Scan(&v.ID, &v.RivianVehicleID, &v.VIN, &v.DisplayName, &v.Model, &v.ModelYear, &v.PackKWh); err != nil {
			return nil, fmt.Errorf("scan user vehicle: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
