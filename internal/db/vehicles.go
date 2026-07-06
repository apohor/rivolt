package db

import (
	"context"
	"database/sql"
	"encoding/json"
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

// ListSubscribableVehicleIDs returns every Rivian gateway vehicle-id
// whose owner currently has a stored Rivian session, i.e. a vehicle we
// have any business holding a subscription lease for.
//
// This is the lease coordinator's authoritative set (SetAuthoritative):
// a lease whose vehicle is absent from it is reaped and unsubscribed.
// Gating on the *stored session* - not merely the vehicles row - is
// what makes disconnect take effect across replicas. Logout deletes the
// user_secrets 'rivian.session' row; on the next reconcile every pod
// sees the vehicle drop out of this set and tears the WS subscription
// down within one cycle. Without it, a logout on one pod left the
// lease-owning pod streaming from its in-memory session until the next
// restart happened to land on it (a user kept being recorded for days
// after disconnecting).
//
// The session predicate is byte-identical to ListUsersWithRivianSession
// (the boot hydrate), so what gets a monitor at startup and what stays
// leased can never disagree. Synthetic electrafi-<hash> import rows are
// excluded - they're not real gateway VINs and never carry a session.
func ListSubscribableVehicleIDs(ctx context.Context, d *sql.DB) ([]string, error) {
	if d == nil {
		return nil, nil
	}
	const q = `
		SELECT DISTINCT v.rivian_vehicle_id
		FROM vehicles v
		JOIN user_secrets s
		  ON s.user_id = v.user_id
		 AND s.name = 'rivian.session'
		WHERE v.rivian_vehicle_id <> ''
		  AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'`
	rows, err := d.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list subscribable vehicles: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			return nil, fmt.Errorf("list subscribable vehicles scan: %w", err)
		}
		out = append(out, vid)
	}
	return out, rows.Err()
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
// excluding the legacy `electrafi-<hash>` synthetic rows left over
// from earlier importers. Synthetic rows linger in existing installs
// because their drives/charges/samples still reference them; we
// hide them from the picker so a fresh import can only land on a
// Rivian-linked vehicle.
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

// VehicleProfile is the user-editable per-vehicle context the
// efficiency analyzer factors into its breakdown. Persisted as
// the "profile" sub-key of the vehicles.metadata JSONB column;
// chosen over typed columns so future fields (e.g. windshield
// state, modified suspension) don't need migrations.
//
// Field semantics:
//   - TireType: "all_season" | "all_terrain" | "winter" | "summer".
//     Empty string means "unset" — the analyzer ignores it.
//   - WheelInches: 0 means unset. Rivian R1 ships 20/21/22.
//   - Accessories: free-form list ("roof_rack", "bike_rack",
//     "rooftop_tent", "ski_box", etc.). Order is not significant.
//   - DefaultExtraLoadLb: persistent extra cargo carried on most
//     drives (e.g. tools, child seat). The per-drive form on the
//     efficiency card adds to this for trip-specific cargo.
//   - FrequentlyTows: hint that the vehicle is regularly used to
//     tow. Doesn't affect the per-drive towing flag, which the
//     analyzer reads independently.
//   - TirePlacardPSI: door-jamb placard pressure in psi. Optional
//     (0 = unset). When supplied, the efficiency prompt cites the
//     delta between current and placard so the model can confidently
//     attribute a "Low tire pressure" factor instead of guessing the
//     placard from generic priors. The user reads this off their
//     door-jamb sticker once.
type VehicleProfile struct {
	TireType           string   `json:"tire_type,omitempty"`
	WheelInches        int      `json:"wheel_inches,omitempty"`
	Accessories        []string `json:"accessories,omitempty"`
	DefaultExtraLoadLb float64  `json:"default_extra_load_lb,omitempty"`
	FrequentlyTows     bool     `json:"frequently_tows,omitempty"`
	TirePlacardPSI     float64  `json:"tire_placard_psi,omitempty"`
	// NativeNACS overrides the model-year heuristic for whether the
	// car has a native NACS port. nil = auto (model_year >= 2026 →
	// native), true = native, false = CCS (needs Tesla adapter).
	NativeNACS *bool `json:"native_nacs,omitempty"`
}

// GetVehicleProfile reads the "profile" sub-key from the given
// vehicle's metadata JSONB. Returns a zero VehicleProfile (not nil)
// when the key is missing or the row has no metadata — callers can
// treat the zero value as "unset" without nil-checking each field.
//
// userID-scoped: a row owned by a different user returns sql.ErrNoRows
// to the caller, which the API layer translates to 404.
func GetVehicleProfile(ctx context.Context, d *sql.DB, userID, vehicleID uuid.UUID) (VehicleProfile, error) {
	var p VehicleProfile
	if d == nil || userID == uuid.Nil || vehicleID == uuid.Nil {
		return p, sql.ErrNoRows
	}
	var raw []byte
	err := d.QueryRowContext(ctx, `
		SELECT COALESCE(metadata->'profile', '{}'::jsonb)::text
		  FROM vehicles
		 WHERE id = $1 AND user_id = $2
	`, vehicleID, userID).Scan(&raw)
	if err != nil {
		return p, err
	}
	if len(raw) == 0 || string(raw) == "{}" {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("vehicle profile decode: %w", err)
	}
	return p, nil
}

// SetVehicleProfile writes the profile struct into the
// vehicles.metadata JSONB at the "profile" key, preserving any
// other top-level metadata fields the row may hold.
func SetVehicleProfile(ctx context.Context, d *sql.DB, userID, vehicleID uuid.UUID, p VehicleProfile) error {
	if d == nil || userID == uuid.Nil || vehicleID == uuid.Nil {
		return sql.ErrNoRows
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("vehicle profile encode: %w", err)
	}
	res, err := d.ExecContext(ctx, `
		UPDATE vehicles
		   SET metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object('profile', $3::jsonb),
		       updated_at = NOW()
		 WHERE id = $1 AND user_id = $2
	`, vehicleID, userID, string(raw))
	if err != nil {
		return fmt.Errorf("vehicle profile update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
