// Package drives persists derived drive sessions. A drive is the
// boundary-framed window of vehicle_state rows where the shift was in
// D/R/N (non-P). Importers and the live ingester both write through
// this store so dashboards can treat drives uniformly regardless of
// origin.
package drives

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS drives (
    id                 TEXT PRIMARY KEY,
    vehicle_id         TEXT NOT NULL,
    started_at         INTEGER NOT NULL,
    ended_at           INTEGER NOT NULL,
    start_soc_pct      REAL,
    end_soc_pct        REAL,
    start_odometer_mi  REAL,
    end_odometer_mi    REAL,
    distance_mi        REAL,
    start_lat          REAL,
    start_lon          REAL,
    end_lat            REAL,
    end_lon            REAL,
    max_speed_mph      REAL,
    avg_speed_mph      REAL,
    source             TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS drives_started_at ON drives(started_at);
CREATE INDEX IF NOT EXISTS drives_vehicle_id ON drives(vehicle_id);
`

// Drive is a single drive session.
type Drive struct {
	ID              string
	VehicleID       string
	StartedAt       time.Time
	EndedAt         time.Time
	StartSoCPct     float64
	EndSoCPct       float64
	StartOdometerMi float64
	EndOdometerMi   float64
	DistanceMi      float64
	StartLat, StartLon float64
	EndLat, EndLon     float64
	MaxSpeedMph     float64
	AvgSpeedMph     float64
	Source          string // "live" | "electrafi_import"
}

// Store wraps access to the drives table.
type Store struct{ db *sql.DB }

// OpenStore opens (or creates) the store at path. Safe to point at the
// same SQLite file used by the rest of the app.
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

// Upsert inserts or replaces a drive by primary key. Used by importers
// which may run repeatedly against the same input file.
func (s *Store) Upsert(ctx context.Context, d Drive) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drives (
			id, vehicle_id, started_at, ended_at,
			start_soc_pct, end_soc_pct,
			start_odometer_mi, end_odometer_mi, distance_mi,
			start_lat, start_lon, end_lat, end_lon,
			max_speed_mph, avg_speed_mph, source
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			vehicle_id         = excluded.vehicle_id,
			started_at         = excluded.started_at,
			ended_at           = excluded.ended_at,
			start_soc_pct      = excluded.start_soc_pct,
			end_soc_pct        = excluded.end_soc_pct,
			start_odometer_mi  = excluded.start_odometer_mi,
			end_odometer_mi    = excluded.end_odometer_mi,
			distance_mi        = excluded.distance_mi,
			start_lat          = excluded.start_lat,
			start_lon          = excluded.start_lon,
			end_lat            = excluded.end_lat,
			end_lon            = excluded.end_lon,
			max_speed_mph      = excluded.max_speed_mph,
			avg_speed_mph      = excluded.avg_speed_mph,
			source             = excluded.source`,
		d.ID, d.VehicleID, d.StartedAt.Unix(), d.EndedAt.Unix(),
		d.StartSoCPct, d.EndSoCPct,
		d.StartOdometerMi, d.EndOdometerMi, d.DistanceMi,
		d.StartLat, d.StartLon, d.EndLat, d.EndLon,
		d.MaxSpeedMph, d.AvgSpeedMph, d.Source,
	)
	return err
}

// Count returns the total number of stored drives.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM drives`).Scan(&n)
	return n, err
}
