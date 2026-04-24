// Package drives persists derived drive sessions.
package drives

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Drive is a single drive session. ID is the importer/recorder's
// stable external id ("electrafi_<hash>_d_<unix>", "live_<vid>_d_<unix>");
// it maps to the drives.external_id column. VehicleID is the Rivian
// gateway string id; the store resolves it to the internal UUID.
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
	Source             string
}

// Store wraps the drives table.
type Store struct {
	db       *sql.DB
	userID   uuid.UUID
	vehicles *db.VehicleResolver
}

// OpenStore binds a pooled connection to a user + vehicle resolver.
func OpenStore(d *sql.DB, userID uuid.UUID, v *db.VehicleResolver) (*Store, error) {
	if d == nil {
		return nil, fmt.Errorf("drives: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("drives: userID is zero")
	}
	if v == nil {
		return nil, fmt.Errorf("drives: vehicle resolver is nil")
	}
	return &Store{db: d, userID: userID, vehicles: v}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Reset deletes every drive for this store's user. Intended for the
// Settings → Danger zone reset button; the session external_id is
// derived from the parsed timestamp, so changing importer inputs
// (tz, pack size) creates parallel rows instead of ON CONFLICT
// upserts. Clearing lets a re-import land on a clean slate.
// Returns the number of rows deleted.
func (s *Store) Reset(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM drives WHERE user_id = $1`, s.userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Upsert inserts or replaces a drive by external_id within the
// (user_id, vehicle_id) scope.
func (s *Store) Upsert(ctx context.Context, d Drive) error {
	vid, err := s.vehicles.Resolve(ctx, d.VehicleID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO drives (
			user_id, vehicle_id, external_id,
			started_at, ended_at,
			start_soc_pct, end_soc_pct,
			start_odometer_mi, end_odometer_mi, distance_mi,
			start_lat, start_lon, end_lat, end_lon,
			max_speed_mph, avg_speed_mph, source
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (vehicle_id, external_id) DO UPDATE SET
			started_at        = EXCLUDED.started_at,
			ended_at          = EXCLUDED.ended_at,
			start_soc_pct     = EXCLUDED.start_soc_pct,
			end_soc_pct       = EXCLUDED.end_soc_pct,
			start_odometer_mi = EXCLUDED.start_odometer_mi,
			end_odometer_mi   = EXCLUDED.end_odometer_mi,
			distance_mi       = EXCLUDED.distance_mi,
			start_lat         = EXCLUDED.start_lat,
			start_lon         = EXCLUDED.start_lon,
			end_lat           = EXCLUDED.end_lat,
			end_lon           = EXCLUDED.end_lon,
			max_speed_mph     = EXCLUDED.max_speed_mph,
			avg_speed_mph     = EXCLUDED.avg_speed_mph,
			source            = EXCLUDED.source,
			updated_at        = NOW()`,
		s.userID, vid, d.ID,
		d.StartedAt.UTC(), d.EndedAt.UTC(),
		nullIfZero(d.StartSoCPct), nullIfZero(d.EndSoCPct),
		nullIfZero(d.StartOdometerMi), nullIfZero(d.EndOdometerMi), nullIfZero(d.DistanceMi),
		nullIfZero(d.StartLat), nullIfZero(d.StartLon),
		nullIfZero(d.EndLat), nullIfZero(d.EndLon),
		nullIfZero(d.MaxSpeedMph), nullIfZero(d.AvgSpeedMph),
		d.Source)
	return err
}

// Count returns the total number of drives for this user.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM drives WHERE user_id = $1`, s.userID).Scan(&n)
	return n, err
}

// ListRecent returns the most recent N drives, newest first.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]Drive, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.external_id, v.rivian_vehicle_id,
		       d.started_at, d.ended_at,
		       COALESCE(d.start_soc_pct,0), COALESCE(d.end_soc_pct,0),
		       COALESCE(d.start_odometer_mi,0), COALESCE(d.end_odometer_mi,0), COALESCE(d.distance_mi,0),
		       COALESCE(d.start_lat,0), COALESCE(d.start_lon,0),
		       COALESCE(d.end_lat,0), COALESCE(d.end_lon,0),
		       COALESCE(d.max_speed_mph,0), COALESCE(d.avg_speed_mph,0), d.source
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1
		ORDER BY d.started_at DESC
		LIMIT $2`, s.userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var d Drive
		if err := rows.Scan(&d.ID, &d.VehicleID, &d.StartedAt, &d.EndedAt,
			&d.StartSoCPct, &d.EndSoCPct,
			&d.StartOdometerMi, &d.EndOdometerMi, &d.DistanceMi,
			&d.StartLat, &d.StartLon, &d.EndLat, &d.EndLon,
			&d.MaxSpeedMph, &d.AvgSpeedMph, &d.Source,
		); err != nil {
			return nil, err
		}
		d.StartedAt = d.StartedAt.UTC()
		d.EndedAt = d.EndedAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// Dedupe is retained for API compatibility but is a no-op on Postgres —
// UNIQUE (vehicle_id, external_id) makes the SQLite "same session
// written with different ids" condition impossible here.
func (s *Store) Dedupe(ctx context.Context) (int, error) { return 0, nil }

// ListAll returns every drive for this user, newest first. Used by
// the Settings → Backup endpoint; /api/drives keeps the capped
// ListRecent path for the dashboard.
func (s *Store) ListAll(ctx context.Context) ([]Drive, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.external_id, v.rivian_vehicle_id,
		       d.started_at, d.ended_at,
		       COALESCE(d.start_soc_pct,0), COALESCE(d.end_soc_pct,0),
		       COALESCE(d.start_odometer_mi,0), COALESCE(d.end_odometer_mi,0), COALESCE(d.distance_mi,0),
		       COALESCE(d.start_lat,0), COALESCE(d.start_lon,0),
		       COALESCE(d.end_lat,0), COALESCE(d.end_lon,0),
		       COALESCE(d.max_speed_mph,0), COALESCE(d.avg_speed_mph,0), d.source
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1
		ORDER BY d.started_at DESC`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var d Drive
		if err := rows.Scan(&d.ID, &d.VehicleID, &d.StartedAt, &d.EndedAt,
			&d.StartSoCPct, &d.EndSoCPct,
			&d.StartOdometerMi, &d.EndOdometerMi, &d.DistanceMi,
			&d.StartLat, &d.StartLon, &d.EndLat, &d.EndLon,
			&d.MaxSpeedMph, &d.AvgSpeedMph, &d.Source,
		); err != nil {
			return nil, err
		}
		d.StartedAt = d.StartedAt.UTC()
		d.EndedAt = d.EndedAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

func nullIfZero(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}
