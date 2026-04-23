// Package samples persists raw vehicle_state polling snapshots — one
// row per poll from any source (live Rivian API, ElectraFi import).
// Derived tables (drives, charges) are computed from samples; keeping
// the raw samples lets us re-derive sessions when logic changes and
// avoids lossy aggregation at ingest time.
package samples

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS vehicle_state (
    vehicle_id        TEXT    NOT NULL,
    at                INTEGER NOT NULL,
    battery_level_pct REAL,
    range_mi          REAL,
    odometer_mi       REAL,
    lat               REAL,
    lon               REAL,
    speed_mph         REAL,
    shift_state       TEXT,
    charging_state    TEXT,
    charger_power_kw  REAL,
    charge_limit_pct  REAL,
    inside_temp_c     REAL,
    outside_temp_c    REAL,
    drive_number      INTEGER,
    charge_number     INTEGER,
    source            TEXT    NOT NULL,
    PRIMARY KEY (vehicle_id, at)
);
CREATE INDEX IF NOT EXISTS vehicle_state_at ON vehicle_state(at);
`

// Sample is a single polling snapshot.
type Sample struct {
	VehicleID       string
	At              time.Time
	BatteryLevelPct float64
	RangeMi         float64
	OdometerMi      float64
	Lat, Lon        float64
	SpeedMph        float64
	ShiftState      string // "P" | "R" | "N" | "D" | ""
	ChargingState   string // "Disconnected" | "Charging" | "Complete" | ...
	ChargerPowerKW  float64
	ChargeLimitPct  float64
	InsideTempC     float64
	OutsideTempC    float64
	DriveNumber     int64
	ChargeNumber    int64
	Source          string // "live" | "electrafi_import"
}

// Store wraps access to the vehicle_state table.
type Store struct{ db *sql.DB }

// OpenStore opens (or creates) the store at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// InsertBatch inserts many samples in a single transaction. Duplicate
// (vehicle_id, at) tuples are ignored so re-imports are idempotent.
func (s *Store) InsertBatch(ctx context.Context, batch []Sample) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO vehicle_state (
			vehicle_id, at,
			battery_level_pct, range_mi, odometer_mi,
			lat, lon, speed_mph, shift_state, charging_state,
			charger_power_kw, charge_limit_pct,
			inside_temp_c, outside_temp_c,
			drive_number, charge_number, source
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range batch {
		if _, err := stmt.ExecContext(ctx,
			v.VehicleID, v.At.Unix(),
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

// Count returns the total number of stored samples.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vehicle_state`).Scan(&n)
	return n, err
}

// ListSince returns samples newer than since, up to limit, ordered by time asc.
// Used by the /api/samples endpoint for sparkline / history rendering.
func (s *Store) ListSince(ctx context.Context, since time.Time, limit int) ([]Sample, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT vehicle_id, at, battery_level_pct, range_mi, odometer_mi,
		       lat, lon, speed_mph, shift_state, charging_state,
		       charger_power_kw, charge_limit_pct,
		       inside_temp_c, outside_temp_c,
		       drive_number, charge_number, source
		FROM vehicle_state
		WHERE at > ?
		ORDER BY at ASC
		LIMIT ?`, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var s Sample
		var atUnix int64
		if err := rows.Scan(&s.VehicleID, &atUnix,
			&s.BatteryLevelPct, &s.RangeMi, &s.OdometerMi,
			&s.Lat, &s.Lon, &s.SpeedMph, &s.ShiftState, &s.ChargingState,
			&s.ChargerPowerKW, &s.ChargeLimitPct,
			&s.InsideTempC, &s.OutsideTempC,
			&s.DriveNumber, &s.ChargeNumber, &s.Source,
		); err != nil {
			return nil, err
		}
		s.At = time.Unix(atUnix, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}
