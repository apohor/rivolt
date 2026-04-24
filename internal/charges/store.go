// Package charges persists derived charge sessions.
package charges

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Charge is a single charging session. ID is the stable external id
// ("live_<vid>_c_<unix>", "electrafi_<vid>_c_<unix>") and maps to the
// charges.external_id column. VehicleID is the Rivian gateway string.
type Charge struct {
	ID             string
	VehicleID      string
	StartedAt      time.Time
	EndedAt        time.Time
	StartSoCPct    float64
	EndSoCPct      float64
	EnergyAddedKWh float64
	MilesAdded     float64
	MaxPowerKW     float64
	AvgPowerKW     float64
	FinalState     string
	Lat, Lon       float64
	Source         string
	Cost           float64
	Currency       string
	PricePerKWh    float64
}

// Store wraps the charges table.
type Store struct {
	db       *sql.DB
	userID   uuid.UUID
	vehicles *db.VehicleResolver
}

// OpenStore binds a pooled connection to a user + vehicle resolver.
func OpenStore(d *sql.DB, userID uuid.UUID, v *db.VehicleResolver) (*Store, error) {
	if d == nil {
		return nil, fmt.Errorf("charges: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("charges: userID is zero")
	}
	if v == nil {
		return nil, fmt.Errorf("charges: vehicle resolver is nil")
	}
	return &Store{db: d, userID: userID, vehicles: v}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Reset deletes every charge for this store's user. See drives.Store.Reset.
func (s *Store) Reset(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM charges WHERE user_id = $1`, s.userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Upsert inserts or replaces a charge by external_id within the
// (user_id, vehicle_id) scope.
func (s *Store) Upsert(ctx context.Context, c Charge) error {
	vid, err := s.vehicles.Resolve(ctx, c.VehicleID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO charges (
			user_id, vehicle_id, external_id,
			started_at, ended_at,
			start_soc_pct, end_soc_pct,
			energy_added_kwh, miles_added,
			max_power_kw, avg_power_kw, final_state,
			lat, lon, source,
			cost, currency, price_per_kwh
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (vehicle_id, external_id) DO UPDATE SET
			started_at       = EXCLUDED.started_at,
			ended_at         = EXCLUDED.ended_at,
			start_soc_pct    = EXCLUDED.start_soc_pct,
			end_soc_pct      = EXCLUDED.end_soc_pct,
			energy_added_kwh = EXCLUDED.energy_added_kwh,
			miles_added      = EXCLUDED.miles_added,
			max_power_kw     = EXCLUDED.max_power_kw,
			avg_power_kw     = EXCLUDED.avg_power_kw,
			final_state      = EXCLUDED.final_state,
			lat              = EXCLUDED.lat,
			lon              = EXCLUDED.lon,
			source           = EXCLUDED.source,
			cost             = EXCLUDED.cost,
			currency         = EXCLUDED.currency,
			price_per_kwh    = EXCLUDED.price_per_kwh,
			updated_at       = NOW()`,
		s.userID, vid, c.ID,
		c.StartedAt.UTC(), c.EndedAt.UTC(),
		nullIfZero(c.StartSoCPct), nullIfZero(c.EndSoCPct),
		nullIfZero(c.EnergyAddedKWh), nullIfZero(c.MilesAdded),
		nullIfZero(c.MaxPowerKW), nullIfZero(c.AvgPowerKW),
		c.FinalState,
		nullIfZero(c.Lat), nullIfZero(c.Lon),
		c.Source,
		nullIfZero(c.Cost), nullIfEmpty(c.Currency), nullIfZero(c.PricePerKWh))
	return err
}

// Count returns the total number of charges for this user.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM charges WHERE user_id = $1`, s.userID).Scan(&n)
	return n, err
}

// ListRecent returns the most recent N charges, newest first.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]Charge, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1
		ORDER BY c.started_at DESC
		LIMIT $2`, s.userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Charge
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.VehicleID,
			&c.StartedAt, &c.EndedAt,
			&c.StartSoCPct, &c.EndSoCPct,
			&c.EnergyAddedKWh, &c.MilesAdded,
			&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
			&c.Lat, &c.Lon, &c.Source,
			&c.Cost, &c.Currency, &c.PricePerKWh,
		); err != nil {
			return nil, err
		}
		c.StartedAt = c.StartedAt.UTC()
		c.EndedAt = c.EndedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestOpenLive returns the most recent live-sourced charge for this
// vehicle whose final_state is still a non-terminal "charging_*" —
// i.e. the recorder was mid-session when the process stopped.
func (s *Store) LatestOpenLive(ctx context.Context, rivianVehicleID string) (*Charge, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT c.external_id, $2::text,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8
		FROM charges c
		WHERE c.user_id = $1 AND c.vehicle_id = $3
		  AND c.source = 'live'
		  AND c.final_state LIKE 'charging\_%' ESCAPE '\'
		  AND c.final_state <> 'charging_complete'
		  AND c.final_state <> 'charging_station_err'
		ORDER BY c.started_at DESC
		LIMIT 1`, s.userID, rivianVehicleID, vid)
	var c Charge
	if err := row.Scan(&c.ID, &c.VehicleID,
		&c.StartedAt, &c.EndedAt,
		&c.StartSoCPct, &c.EndSoCPct,
		&c.EnergyAddedKWh, &c.MilesAdded,
		&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
		&c.Lat, &c.Lon, &c.Source,
		&c.Cost, &c.Currency, &c.PricePerKWh,
	); err != nil {
		return nil, err
	}
	c.StartedAt = c.StartedAt.UTC()
	c.EndedAt = c.EndedAt.UTC()
	return &c, nil
}

// CloseStaleOpenLive marks every live-sourced charge for the vehicle
// whose external_id is NOT keepID and whose final_state is still a
// non-terminal "charging_*" as abandoned. Returns rows updated.
func (s *Store) CloseStaleOpenLive(ctx context.Context, rivianVehicleID, keepID string) (int, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET final_state = 'abandoned', updated_at = NOW()
		WHERE user_id = $1 AND vehicle_id = $2
		  AND source = 'live'
		  AND external_id <> $3
		  AND final_state LIKE 'charging\_%' ESCAPE '\'
		  AND final_state <> 'charging_complete'
		  AND final_state <> 'charging_station_err'`,
		s.userID, vid, keepID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Dedupe is a no-op on Postgres — UNIQUE (vehicle_id, external_id)
// prevents the SQLite-era duplicates the old code had to clean up.
func (s *Store) Dedupe(ctx context.Context) (int, error) { return 0, nil }

// ListAll returns every charge for this user, newest first. Used by
// the Settings → Backup endpoint.
func (s *Store) ListAll(ctx context.Context) ([]Charge, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.external_id, v.rivian_vehicle_id,
		       c.started_at, c.ended_at,
		       COALESCE(c.start_soc_pct,0), COALESCE(c.end_soc_pct,0),
		       COALESCE(c.energy_added_kwh,0), COALESCE(c.miles_added,0),
		       COALESCE(c.max_power_kw,0), COALESCE(c.avg_power_kw,0),
		       COALESCE(c.final_state,''),
		       COALESCE(c.lat,0), COALESCE(c.lon,0),
		       c.source,
		       COALESCE(c.cost,0)::float8, COALESCE(c.currency,''),
		       COALESCE(c.price_per_kwh,0)::float8
		FROM charges c
		JOIN vehicles v ON v.id = c.vehicle_id
		WHERE c.user_id = $1
		ORDER BY c.started_at DESC`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Charge
	for rows.Next() {
		var c Charge
		if err := rows.Scan(&c.ID, &c.VehicleID,
			&c.StartedAt, &c.EndedAt,
			&c.StartSoCPct, &c.EndSoCPct,
			&c.EnergyAddedKWh, &c.MilesAdded,
			&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
			&c.Lat, &c.Lon, &c.Source,
			&c.Cost, &c.Currency, &c.PricePerKWh,
		); err != nil {
			return nil, err
		}
		c.StartedAt = c.StartedAt.UTC()
		c.EndedAt = c.EndedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullIfZero(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
