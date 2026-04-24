// Package samples persists raw vehicle_state polling snapshots.
package samples

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Sample is a single polling snapshot. VehicleID is the Rivian gateway
// string id ("01-242521064"); the store resolves it to the internal
// vehicles.id UUID on write.
type Sample struct {
	VehicleID       string
	At              time.Time
	BatteryLevelPct float64
	RangeMi         float64
	OdometerMi      float64
	Lat, Lon        float64
	SpeedMph        float64
	ShiftState      string
	ChargingState   string
	ChargerPowerKW  float64
	ChargeLimitPct  float64
	InsideTempC     float64
	OutsideTempC    float64
	DriveNumber     int64
	ChargeNumber    int64
	Source          string // "live" | "electrafi_import"
}

// Store wraps the vehicle_state table, scoped to one user.
type Store struct {
	db       *sql.DB
	userID   uuid.UUID
	vehicles *db.VehicleResolver
}

// OpenStore binds a pooled connection to a user + vehicle resolver.
func OpenStore(d *sql.DB, userID uuid.UUID, v *db.VehicleResolver) (*Store, error) {
	if d == nil {
		return nil, fmt.Errorf("samples: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("samples: userID is zero")
	}
	if v == nil {
		return nil, fmt.Errorf("samples: vehicle resolver is nil")
	}
	return &Store{db: d, userID: userID, vehicles: v}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Reset deletes every raw sample (vehicle_state row) for this
// store's user. See drives.Store.Reset.
func (s *Store) Reset(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM vehicle_state WHERE user_id = $1`, s.userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// InsertBatch inserts many samples in a single transaction.
// Duplicate (vehicle_id, at) tuples are ignored so re-imports are
// idempotent. All samples must belong to the same user.
func (s *Store) InsertBatch(ctx context.Context, batch []Sample) error {
	if len(batch) == 0 {
		return nil
	}
	// Resolve every distinct Rivian id outside the tx so a new
	// vehicle row is committed independently of the sample write.
	uuids := make(map[string]uuid.UUID, len(batch))
	for _, v := range batch {
		if _, ok := uuids[v.VehicleID]; ok {
			continue
		}
		id, err := s.vehicles.Resolve(ctx, v.VehicleID)
		if err != nil {
			return err
		}
		uuids[v.VehicleID] = id
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO vehicle_state (
			user_id, vehicle_id, at,
			battery_level_pct, range_mi, odometer_mi,
			lat, lon, speed_mph, shift_state, charging_state,
			charger_power_kw, charge_limit_pct,
			inside_temp_c, outside_temp_c,
			drive_number, charge_number, source
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (vehicle_id, at) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range batch {
		if _, err := stmt.ExecContext(ctx,
			s.userID, uuids[v.VehicleID], v.At.UTC(),
			v.BatteryLevelPct, v.RangeMi, v.OdometerMi,
			v.Lat, v.Lon, v.SpeedMph, v.ShiftState, v.ChargingState,
			v.ChargerPowerKW, v.ChargeLimitPct,
			v.InsideTempC, v.OutsideTempC,
			v.DriveNumber, v.ChargeNumber, v.Source,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Count returns the total number of samples for this user.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vehicle_state WHERE user_id = $1`,
		s.userID).Scan(&n)
	return n, err
}

// ListSince returns samples newer than since, up to limit, oldest first.
func (s *Store) ListSince(ctx context.Context, since time.Time, limit int) ([]Sample, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.rivian_vehicle_id, vs.at,
		       COALESCE(vs.battery_level_pct,0), COALESCE(vs.range_mi,0), COALESCE(vs.odometer_mi,0),
		       COALESCE(vs.lat,0), COALESCE(vs.lon,0), COALESCE(vs.speed_mph,0),
		       COALESCE(vs.shift_state,''), COALESCE(vs.charging_state,''),
		       COALESCE(vs.charger_power_kw,0), COALESCE(vs.charge_limit_pct,0),
		       COALESCE(vs.inside_temp_c,0), COALESCE(vs.outside_temp_c,0),
		       COALESCE(vs.drive_number,0), COALESCE(vs.charge_number,0), vs.source
		FROM vehicle_state vs
		JOIN vehicles v ON v.id = vs.vehicle_id
		WHERE vs.user_id = $1 AND vs.at > $2
		ORDER BY vs.at ASC
		LIMIT $3`, s.userID, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var v Sample
		if err := rows.Scan(&v.VehicleID, &v.At,
			&v.BatteryLevelPct, &v.RangeMi, &v.OdometerMi,
			&v.Lat, &v.Lon, &v.SpeedMph, &v.ShiftState, &v.ChargingState,
			&v.ChargerPowerKW, &v.ChargeLimitPct,
			&v.InsideTempC, &v.OutsideTempC,
			&v.DriveNumber, &v.ChargeNumber, &v.Source,
		); err != nil {
			return nil, err
		}
		v.At = v.At.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListAll returns every sample for this user, oldest first. Used by
// the Settings → Backup endpoint. Unbounded — a year of 60 s polls
// is on the order of 500k rows, ~100 MB JSON; acceptable for a
// homelab backup but NOT suitable for UI consumption.
func (s *Store) ListAll(ctx context.Context) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.rivian_vehicle_id, vs.at,
		       COALESCE(vs.battery_level_pct,0), COALESCE(vs.range_mi,0), COALESCE(vs.odometer_mi,0),
		       COALESCE(vs.lat,0), COALESCE(vs.lon,0), COALESCE(vs.speed_mph,0),
		       COALESCE(vs.shift_state,''), COALESCE(vs.charging_state,''),
		       COALESCE(vs.charger_power_kw,0), COALESCE(vs.charge_limit_pct,0),
		       COALESCE(vs.inside_temp_c,0), COALESCE(vs.outside_temp_c,0),
		       COALESCE(vs.drive_number,0), COALESCE(vs.charge_number,0), vs.source
		FROM vehicle_state vs
		JOIN vehicles v ON v.id = vs.vehicle_id
		WHERE vs.user_id = $1
		ORDER BY vs.at ASC`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var v Sample
		if err := rows.Scan(&v.VehicleID, &v.At,
			&v.BatteryLevelPct, &v.RangeMi, &v.OdometerMi,
			&v.Lat, &v.Lon, &v.SpeedMph, &v.ShiftState, &v.ChargingState,
			&v.ChargerPowerKW, &v.ChargeLimitPct,
			&v.InsideTempC, &v.OutsideTempC,
			&v.DriveNumber, &v.ChargeNumber, &v.Source,
		); err != nil {
			return nil, err
		}
		v.At = v.At.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}
