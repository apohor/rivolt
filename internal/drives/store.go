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
	ID                 string
	VehicleID          string
	StartedAt          time.Time
	EndedAt            time.Time
	StartSoCPct        float64
	EndSoCPct          float64
	StartOdometerMi    float64
	EndOdometerMi      float64
	DistanceMi         float64
	StartLat, StartLon float64
	EndLat, EndLon     float64
	MaxSpeedMph        float64
	AvgSpeedMph        float64
	Source             string // "live" | "electrafi_import"
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

// ListRecent returns the most recent N drives, newest first. Used by
// the /api/drives endpoint for dashboard rendering.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]Drive, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, vehicle_id, started_at, ended_at,
		       start_soc_pct, end_soc_pct,
		       start_odometer_mi, end_odometer_mi, distance_mi,
		       start_lat, start_lon, end_lat, end_lon,
		       max_speed_mph, avg_speed_mph, source
		FROM drives ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var d Drive
		var startUnix, endUnix int64
		if err := rows.Scan(&d.ID, &d.VehicleID, &startUnix, &endUnix,
			&d.StartSoCPct, &d.EndSoCPct,
			&d.StartOdometerMi, &d.EndOdometerMi, &d.DistanceMi,
			&d.StartLat, &d.StartLon, &d.EndLat, &d.EndLon,
			&d.MaxSpeedMph, &d.AvgSpeedMph, &d.Source,
		); err != nil {
			return nil, err
		}
		d.StartedAt = time.Unix(startUnix, 0).UTC()
		d.EndedAt = time.Unix(endUnix, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// Dedupe collapses duplicate drive rows. Two passes, mirroring the
// charges store: electrafi_import rows group by started_at alone
// (early versions hashed the CSV filename into vehicle_id so re-imports
// from differently-named exports produced different vehicle_ids for
// the same physical drive), everything else groups by
// (vehicle_id, started_at).
//
// Returns the number of rows deleted.
func (s *Store) Dedupe(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	total := 0

	// Pass 1: electrafi_import rows, grouped by started_at alone.
	{
		if _, err := tx.ExecContext(ctx, `
			WITH ranked AS (
				SELECT id, started_at,
				       ROW_NUMBER() OVER (
				         PARTITION BY started_at
				         ORDER BY distance_mi DESC,
				                  max_speed_mph DESC,
				                  ended_at DESC,
				                  id ASC
				       ) AS rn
				FROM drives
				WHERE source = 'electrafi_import'
			),
			keepers AS (SELECT id, started_at FROM ranked WHERE rn = 1),
			merges AS (
				SELECT k.id AS keep_id,
				       MAX(d.distance_mi)   AS distance_mi,
				       MAX(d.max_speed_mph) AS max_speed_mph,
				       MAX(d.avg_speed_mph) AS avg_speed_mph,
				       MAX(d.ended_at)      AS ended_at
				FROM keepers k
				JOIN drives d
				  ON d.started_at = k.started_at
				 AND d.source = 'electrafi_import'
				GROUP BY k.id
			)
			UPDATE drives SET
				distance_mi   = (SELECT distance_mi   FROM merges WHERE merges.keep_id = drives.id),
				max_speed_mph = (SELECT max_speed_mph FROM merges WHERE merges.keep_id = drives.id),
				avg_speed_mph = (SELECT avg_speed_mph FROM merges WHERE merges.keep_id = drives.id),
				ended_at      = (SELECT ended_at      FROM merges WHERE merges.keep_id = drives.id)
			WHERE id IN (SELECT keep_id FROM merges)
		`); err != nil {
			return 0, fmt.Errorf("electrafi merge: %w", err)
		}
		res, err := tx.ExecContext(ctx, `
			DELETE FROM drives WHERE id IN (
				SELECT id FROM (
					SELECT id, ROW_NUMBER() OVER (
						PARTITION BY started_at
						ORDER BY distance_mi DESC,
						         max_speed_mph DESC,
						         ended_at DESC,
						         id ASC
					) AS rn FROM drives
					WHERE source = 'electrafi_import'
				) WHERE rn > 1
			)`)
		if err != nil {
			return 0, fmt.Errorf("electrafi delete: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}

	// Pass 2: everything else, grouped by (vehicle_id, started_at).
	if _, err := tx.ExecContext(ctx, `
		WITH ranked AS (
			SELECT id, vehicle_id, started_at,
			       ROW_NUMBER() OVER (
			         PARTITION BY vehicle_id, started_at
			         ORDER BY distance_mi DESC,
			                  max_speed_mph DESC,
			                  ended_at DESC,
			                  id ASC
			       ) AS rn
			FROM drives
			WHERE source <> 'electrafi_import'
		)
		DELETE FROM drives WHERE id IN (SELECT id FROM ranked WHERE rn > 1)
	`); err != nil {
		return 0, fmt.Errorf("live delete: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}
