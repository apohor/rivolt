// Package drives persists derived drive sessions.
package drives

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/histfloor"
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
	// EnergyUsedKWh is the pack-side energy consumed over the drive,
	// derived from (startSoC - endSoC)/100 × usable pack capacity.
	// Zero on legacy rows imported before migration 0002.
	EnergyUsedKWh float64
	Source        string
	// RoutePolyline is the drive's GPS path encoded with the
	// Google polyline algorithm at precision 5 (~1m resolution).
	// Empty for legacy ElectraFi imports and pre-migration live
	// drives -- consumers fall back to a straight start->end
	// line when this is empty.
	RoutePolyline string
	// Thinned is set by Get and the lists (not by the backup/export iterators):
	// the drive's month of raw telemetry has been thinned (sample_tiers) and is not
	// currently held at full detail by an admin restore, so the detail the drive
	// page draws from it is the lighter recording. Absent when not. The month is
	// UTC, as the partitions are; a drive that crosses a month counts as thinned
	// when either of its months is.
	Thinned bool `json:"thinned,omitempty"`
	// Trailer is what the recorder saw: the trailer connector was present for at least half of the time its state was
	// known, absent for more of it, or nil when it was never known. The connector, not the load.
	Trailer *bool
	// TrailerOverride is the user's own correction, nil when they have made none. Backup and restore carry both.
	TrailerOverride *bool
	// LocationUnavailable: the drive moved but its truck sent no usable live location, so it has no route (a truck whose
	// location sharing is off, or whose GPS is not updating). Set by the live recorder; kept in drives.metadata.
	LocationUnavailable bool
}

// HasTrailer is the effective value: the user's correction when there is one, else what the recorder saw.
func (d Drive) HasTrailer() *bool {
	if d.TrailerOverride != nil {
		return d.TrailerOverride
	}
	return d.Trailer
}

// thinnedSQL is the column that fills Drive.Thinned: one of the drive's months
// (start or end, UTC) has a sample_tiers row and is not held at full detail for
// this vehicle (archive_months.rethin_after in the future, set by an admin
// restore; the full rows are back for those days).
const thinnedSQL = `EXISTS (SELECT 1 FROM sample_tiers t
		        WHERE t.month IN (date_trunc('month', d.started_at AT TIME ZONE 'UTC')::date,
		                          date_trunc('month', d.ended_at AT TIME ZONE 'UTC')::date)
		          AND NOT EXISTS (SELECT 1 FROM archive_months a
		                           WHERE a.user_id = d.user_id AND a.vehicle_id = d.vehicle_id
		                             AND a.month = t.month AND a.rethin_after > now()))`

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

// Reset deletes every drive for this store's user, and the per-drive rows
// derived from them. Intended for the Settings → Danger zone reset button;
// the session external_id is derived from the parsed timestamp, so changing
// importer inputs (tz, pack size) creates parallel rows instead of ON
// CONFLICT upserts. Clearing lets a re-import land on a clean slate.
//
// The recap, efficiency and weather rows are keyed (user_id, drive_id =
// drives.external_id) with no foreign key to drives, so deleting the drives
// does not remove them. They hold the drive's coarse start location and hour
// (weather) and generated text (recaps), so "delete your history" removes them
// in the same transaction. Returns the number of drives deleted.
func (s *Store) Reset(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, t := range perDriveTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE user_id = $1`, s.userID); err != nil {
			return 0, fmt.Errorf("reset %s: %w", t, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM drives WHERE user_id = $1`, s.userID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteBefore deletes this user's drives that started before cutoff, with the per-drive rows that hang off them (see Reset), in
// one transaction. Returns the number of drives deleted.
func (s *Store) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, t := range perDriveTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE user_id = $1 AND drive_id IN
			(SELECT external_id FROM drives WHERE user_id = $1 AND started_at < $2)`, s.userID, cutoff); err != nil {
			return 0, fmt.Errorf("delete before, %s: %w", t, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM drives WHERE user_id = $1 AND started_at < $2`, s.userID, cutoff)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// perDriveTables are the tables keyed (user_id, drive_id) that hang off a drive
// without a foreign key (see Reset).
var perDriveTables = []string{"drive_recaps", "drive_efficiency", "drive_weather", "drive_weather_series"}

// driveColumns is the column list Upsert writes, in args order.
const driveColumns = `
			user_id, vehicle_id, external_id,
			started_at, ended_at,
			start_soc_pct, end_soc_pct,
			start_odometer_mi, end_odometer_mi, distance_mi,
			start_lat, start_lon, end_lat, end_lon,
			max_speed_mph, avg_speed_mph, energy_used_kwh, source,
			route_polyline, trailer, trailer_override, metadata`

// driveOnConflict is the update half of the upsert, shared by the plain and the guarded statement.
const driveOnConflict = `
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
			energy_used_kwh   = EXCLUDED.energy_used_kwh,
			source            = EXCLUDED.source,
			-- Only overwrite route_polyline when the new row carries
			-- one. ElectraFi re-imports of a row that the live
			-- recorder already populated shouldn't erase the polyline.
			route_polyline    = COALESCE(EXCLUDED.route_polyline, drives.route_polyline),
			-- A recording that never knew the trailer state (NULL) leaves what is there; the user's correction is only
			-- ever set by a value (a restore) or SetTrailerOverride, never cleared by a re-recording.
			trailer           = COALESCE(EXCLUDED.trailer, drives.trailer),
			trailer_override  = COALESCE(EXCLUDED.trailer_override, drives.trailer_override),
			-- The live recorder knows whether the truck reported its location: its row sets or clears the flag. Any other source
			-- (an import, a restore of another kind) leaves the metadata as it is.
			metadata          = CASE WHEN EXCLUDED.source = 'live' THEN (drives.metadata - 'location_unavailable') || EXCLUDED.metadata ELSE drives.metadata END,
			updated_at        = NOW()`

// guardedDriveUpsert writes the drive only if rivolt_lease_ok(group, pod, epoch) holds when the
// statement starts, and says in the same statement whether it was refused: (false, 0) refused,
// (true, 1) written. The row travels as one JSON document so the columns keep their own types
// (json_populate_record) with no per-column casts.
const guardedDriveUpsert = `
	WITH ok AS MATERIALIZED (SELECT rivolt_lease_ok($1::uuid, $2::text, $3::bigint) AS ok),
	ins AS (
		INSERT INTO drives (` + driveColumns + `
		) SELECT r.user_id, r.vehicle_id, r.external_id, r.started_at, r.ended_at,
		         r.start_soc_pct, r.end_soc_pct, r.start_odometer_mi, r.end_odometer_mi, r.distance_mi,
		         r.start_lat, r.start_lon, r.end_lat, r.end_lon,
		         r.max_speed_mph, r.avg_speed_mph, r.energy_used_kwh, r.source,
		         r.route_polyline, r.trailer, r.trailer_override, COALESCE(r.metadata, '{}'::jsonb)
		  FROM ok, json_populate_record(NULL::drives, $4::json) r WHERE ok.ok` + driveOnConflict + `
		RETURNING 1)
	SELECT (SELECT ok FROM ok), (SELECT count(*) FROM ins)`

// Upsert inserts or replaces a drive by external_id within the
// (user_id, vehicle_id) scope. With a fence token in the context (the live recorder of a pod
// that holds the group lease) the write is guarded by the lease in the same statement and
// returns fence.ErrFenced when the lease is gone.
func (s *Store) Upsert(ctx context.Context, d Drive) error {
	// A drive that starts before the user's history floor is deleted history coming back (a late close, a catch-up): dropped whole,
	// not cut, so no half of it survives. A user's own import or restore passes histfloor.Bypass.
	if below, err := histfloor.Below(ctx, s.db, s.userID, d.StartedAt); err != nil {
		return err
	} else if below {
		return nil
	}
	vid, err := s.vehicles.Resolve(ctx, d.VehicleID)
	if err != nil {
		return err
	}
	if tok, ok := fence.From(ctx); ok {
		doc, err := json.Marshal(map[string]any{
			"user_id": s.userID, "vehicle_id": vid, "external_id": d.ID,
			"started_at": d.StartedAt.UTC(), "ended_at": d.EndedAt.UTC(),
			"start_soc_pct": jsonNum(d.StartSoCPct), "end_soc_pct": jsonNum(d.EndSoCPct),
			"start_odometer_mi": jsonNum(d.StartOdometerMi), "end_odometer_mi": jsonNum(d.EndOdometerMi),
			"distance_mi": jsonNum(d.DistanceMi),
			"start_lat":   jsonNum(d.StartLat), "start_lon": jsonNum(d.StartLon),
			"end_lat": jsonNum(d.EndLat), "end_lon": jsonNum(d.EndLon),
			"max_speed_mph": jsonNum(d.MaxSpeedMph), "avg_speed_mph": jsonNum(d.AvgSpeedMph),
			"energy_used_kwh": jsonNum(d.EnergyUsedKWh), "source": d.Source,
			"route_polyline": jsonStr(d.RoutePolyline), "trailer": d.Trailer, "trailer_override": d.TrailerOverride,
			"metadata": driveMetadata(d),
		})
		if err != nil {
			return err
		}
		var allowed bool
		var n int
		// In a bounded transaction (2 s cap, 2 s statement timeout): an upsert that passed the lease
		// check and then waits on a row lock is cancelled, not committed after the lease expired.
		if err := fence.InTx(ctx, s.db, func(tctx context.Context, tx *sql.Tx) error {
			return tx.QueryRowContext(tctx, guardedDriveUpsert, tok.Group, tok.Pod, tok.Epoch, string(doc)).Scan(&allowed, &n)
		}); err != nil {
			return err
		}
		if !allowed {
			return fence.ErrFenced
		}
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO drives (`+driveColumns+`
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22::jsonb)`+driveOnConflict,
		s.userID, vid, d.ID,
		d.StartedAt.UTC(), d.EndedAt.UTC(),
		nullIfZero(d.StartSoCPct), nullIfZero(d.EndSoCPct),
		nullIfZero(d.StartOdometerMi), nullIfZero(d.EndOdometerMi), nullIfZero(d.DistanceMi),
		nullIfZero(d.StartLat), nullIfZero(d.StartLon),
		nullIfZero(d.EndLat), nullIfZero(d.EndLon),
		nullIfZero(d.MaxSpeedMph), nullIfZero(d.AvgSpeedMph),
		nullIfZero(d.EnergyUsedKWh),
		d.Source,
		nullIfEmpty(d.RoutePolyline),
		d.Trailer, d.TrailerOverride, mustJSON(driveMetadata(d)))
	return err
}

// SetTrailerOverride sets the user's own correction on one of their drives (by external id): true or false, or nil
// to go back to what was recorded. Reports whether the user has such a drive.
func (s *Store) SetTrailerOverride(ctx context.Context, externalID string, v *bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE drives SET trailer_override = $3, updated_at = NOW()
		 WHERE user_id = $1 AND external_id = $2`, s.userID, externalID, v)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
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
	return s.listRecent(ctx, limit, true, "")
}

// ErrRangeTooLarge: a window holds more rows than one answer may carry.
var ErrRangeTooLarge = errors.New("window holds more rows than one answer carries")

// ListRange returns the user's listed drives (as ListRecent: a minute or more) that started in [from, to), newest first, from
// every truck, without the route. At most maxRangeRows; a window with more returns ErrRangeTooLarge rather than a partial list,
// for totals that must not be short.
func (s *Store) ListRange(ctx context.Context, from, to time.Time) ([]Drive, error) {
	rows, err := s.listWindow(ctx, maxRangeRows+1, false, "", &from, &to)
	if err != nil {
		return nil, err
	}
	if len(rows) > maxRangeRows {
		return nil, ErrRangeTooLarge
	}
	return rows, nil
}

var maxRangeRows = 9000

// ListRecentForVehicle is ListRecent for one of the user's trucks, by its
// Rivian vehicle id; polyline false leaves out the route as ListRecentSlim.
// An id the user doesn't have returns no rows.
func (s *Store) ListRecentForVehicle(ctx context.Context, rivianVehicleID string, limit int, polyline bool) ([]Drive, error) {
	return s.listRecent(ctx, limit, polyline, rivianVehicleID)
}

// ListRecentSlim is ListRecent without the route polyline (RoutePolyline is
// empty): the lists don't draw maps, and the polyline is most of a row's size.
// Get returns the full row.
func (s *Store) ListRecentSlim(ctx context.Context, limit int) ([]Drive, error) {
	return s.listRecent(ctx, limit, false, "")
}

// listRecent lists the user's drives, newest first; rivianVehicleID, when set,
// keeps one truck's.
func (s *Store) listRecent(ctx context.Context, limit int, polyline bool, rivianVehicleID string) ([]Drive, error) {
	return s.listWindow(ctx, limit, polyline, rivianVehicleID, nil, nil)
}

// listWindow is listRecent narrowed to drives that started in [from, to) when those are set.
func (s *Store) listWindow(ctx context.Context, limit int, polyline bool, rivianVehicleID string, from, to *time.Time) ([]Drive, error) {
	routeCol := `''`
	if polyline {
		routeCol = `COALESCE(d.route_polyline,'')`
	}
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
		       COALESCE(d.max_speed_mph,0), COALESCE(d.avg_speed_mph,0),
		       COALESCE(d.energy_used_kwh,0), d.source,
		       `+routeCol+`, `+thinnedSQL+`, d.trailer, d.trailer_override, `+locationUnavailableSQL+`
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1
		  AND ended_at - started_at >= interval '1 minute'
		  AND ($3::text = '' OR v.rivian_vehicle_id = $3::text)
		  AND ($4::timestamptz IS NULL OR d.started_at >= $4::timestamptz)
		  AND ($5::timestamptz IS NULL OR d.started_at < $5::timestamptz)
		ORDER BY d.started_at DESC
		LIMIT $2`, s.userID, limit, rivianVehicleID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var d Drive
		var tr, tro sql.NullBool
		if err := rows.Scan(&d.ID, &d.VehicleID, &d.StartedAt, &d.EndedAt,
			&d.StartSoCPct, &d.EndSoCPct,
			&d.StartOdometerMi, &d.EndOdometerMi, &d.DistanceMi,
			&d.StartLat, &d.StartLon, &d.EndLat, &d.EndLon,
			&d.MaxSpeedMph, &d.AvgSpeedMph,
			&d.EnergyUsedKWh, &d.Source,
			&d.RoutePolyline, &d.Thinned, &tr, &tro, &d.LocationUnavailable,
		); err != nil {
			return nil, err
		}
		d.Trailer, d.TrailerOverride = nullBoolPtr(tr), nullBoolPtr(tro)
		d.StartedAt = d.StartedAt.UTC()
		d.EndedAt = d.EndedAt.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get returns one drive by its external id, with its route polyline, or nil
// when the user has no such drive.
func (s *Store) Get(ctx context.Context, externalID string) (*Drive, error) {
	var d Drive
	var tr, tro sql.NullBool
	err := s.db.QueryRowContext(ctx, `
		SELECT d.external_id, v.rivian_vehicle_id,
		       d.started_at, d.ended_at,
		       COALESCE(d.start_soc_pct,0), COALESCE(d.end_soc_pct,0),
		       COALESCE(d.start_odometer_mi,0), COALESCE(d.end_odometer_mi,0), COALESCE(d.distance_mi,0),
		       COALESCE(d.start_lat,0), COALESCE(d.start_lon,0),
		       COALESCE(d.end_lat,0), COALESCE(d.end_lon,0),
		       COALESCE(d.max_speed_mph,0), COALESCE(d.avg_speed_mph,0),
		       COALESCE(d.energy_used_kwh,0), d.source,
		       COALESCE(d.route_polyline,''), `+thinnedSQL+`, d.trailer, d.trailer_override, `+locationUnavailableSQL+`
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1 AND d.external_id = $2
		ORDER BY d.started_at DESC LIMIT 1`, s.userID, externalID).Scan(
		&d.ID, &d.VehicleID, &d.StartedAt, &d.EndedAt,
		&d.StartSoCPct, &d.EndSoCPct,
		&d.StartOdometerMi, &d.EndOdometerMi, &d.DistanceMi,
		&d.StartLat, &d.StartLon, &d.EndLat, &d.EndLon,
		&d.MaxSpeedMph, &d.AvgSpeedMph,
		&d.EnergyUsedKWh, &d.Source,
		&d.RoutePolyline, &d.Thinned, &tr, &tro, &d.LocationUnavailable)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.Trailer, d.TrailerOverride = nullBoolPtr(tr), nullBoolPtr(tro)
	d.StartedAt = d.StartedAt.UTC()
	d.EndedAt = d.EndedAt.UTC()
	return &d, nil
}

// Dedupe is retained for API compatibility but is a no-op on Postgres —
// UNIQUE (vehicle_id, external_id) makes the SQLite "same session
// written with different ids" condition impossible here.
func (s *Store) Dedupe(ctx context.Context) (int, error) { return 0, nil }

// EachAll streams EVERY stored drive of this user, newest first, with no
// display filter: the backup and the data export must hold every row, including
// the sub-minute ones the lists hide. Callers that show drives (efficiency,
// weather backfill) use EachListed.
func (s *Store) EachAll(ctx context.Context, fn func(Drive) error) error {
	return s.each(ctx, false, fn)
}

// EachListed is EachAll without the drives the lists hide: those under a minute.
func (s *Store) EachListed(ctx context.Context, fn func(Drive) error) error {
	return s.each(ctx, true, fn)
}

func (s *Store) each(ctx context.Context, listedOnly bool, fn func(Drive) error) error {
	minDur := `ended_at - started_at >= interval '1 minute'`
	if !listedOnly {
		minDur = `TRUE`
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.external_id, v.rivian_vehicle_id,
		       d.started_at, d.ended_at,
		       COALESCE(d.start_soc_pct,0), COALESCE(d.end_soc_pct,0),
		       COALESCE(d.start_odometer_mi,0), COALESCE(d.end_odometer_mi,0), COALESCE(d.distance_mi,0),
		       COALESCE(d.start_lat,0), COALESCE(d.start_lon,0),
		       COALESCE(d.end_lat,0), COALESCE(d.end_lon,0),
		       COALESCE(d.max_speed_mph,0), COALESCE(d.avg_speed_mph,0),
		       COALESCE(d.energy_used_kwh,0), d.source,
		       COALESCE(d.route_polyline,''), d.trailer, d.trailer_override, `+locationUnavailableSQL+`
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1
		  AND `+minDur+`
		ORDER BY d.started_at DESC`, s.userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d Drive
		var tr, tro sql.NullBool
		if err := rows.Scan(&d.ID, &d.VehicleID, &d.StartedAt, &d.EndedAt,
			&d.StartSoCPct, &d.EndSoCPct,
			&d.StartOdometerMi, &d.EndOdometerMi, &d.DistanceMi,
			&d.StartLat, &d.StartLon, &d.EndLat, &d.EndLon,
			&d.MaxSpeedMph, &d.AvgSpeedMph,
			&d.EnergyUsedKWh, &d.Source,
			&d.RoutePolyline, &tr, &tro, &d.LocationUnavailable,
		); err != nil {
			return err
		}
		d.Trailer, d.TrailerOverride = nullBoolPtr(tr), nullBoolPtr(tro)
		d.StartedAt = d.StartedAt.UTC()
		d.EndedAt = d.EndedAt.UTC()
		if err := fn(d); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ListAll returns every stored row; use EachAll for large result sets.
func (s *Store) ListAll(ctx context.Context) ([]Drive, error) {
	var out []Drive
	err := s.EachAll(ctx, func(v Drive) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

// ListAllListed returns the drives the lists show (a minute or longer).
func (s *Store) ListAllListed(ctx context.Context) ([]Drive, error) {
	var out []Drive
	err := s.EachListed(ctx, func(v Drive) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

// nullBoolPtr is a nullable boolean column as a pointer: nil when NULL.
func nullBoolPtr(b sql.NullBool) *bool {
	if !b.Valid {
		return nil
	}
	v := b.Bool
	return &v
}

func nullIfZero(f float64) sql.NullFloat64 {
	if f == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: f, Valid: true}
}

// jsonNum and jsonStr are nullIfZero and nullIfEmpty for the JSON document of the guarded upsert
// (sql.Null* marshal as structs, not as values).
func jsonNum(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func jsonStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// LiveOverlap describes the live drive another recorder wrote over the
// same time range: where it ends and its end readings.
type LiveOverlap struct {
	EndedAt       time.Time
	EndSoCPct     float64
	EndOdometerMi float64
}

// LiveOverlap returns the latest-ending live drive for this vehicle,
// other than excludeID, whose time range overlaps [start, end), or nil.
// See charges.Store.LiveOverlap.
func (s *Store) LiveOverlap(ctx context.Context, rivianVehicleID, excludeID string, start, end time.Time) (*LiveOverlap, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return nil, err
	}
	var o LiveOverlap
	err = s.db.QueryRowContext(ctx, `
		SELECT ended_at, COALESCE(end_soc_pct, 0), COALESCE(end_odometer_mi, 0)
		  FROM drives
		 WHERE user_id = $1 AND vehicle_id = $2 AND source = 'live' AND external_id <> $3
		   AND tstzrange(started_at, GREATEST(ended_at, started_at)) && tstzrange($4::timestamptz, GREATEST($5::timestamptz, $4::timestamptz))
		 ORDER BY ended_at DESC
		 LIMIT 1`, s.userID, vid, excludeID, start.UTC(), end.UTC()).Scan(&o.EndedAt, &o.EndSoCPct, &o.EndOdometerMi)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.EndedAt = o.EndedAt.UTC()
	return &o, nil
}

// LiveCovers reports whether a live (recorder) drive for this vehicle
// overlaps [start, end). See charges.Store.LiveCovers.
func (s *Store) LiveCovers(ctx context.Context, rivianVehicleID string, start, end time.Time) (bool, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return false, err
	}
	var ok bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM drives
		   WHERE user_id = $1 AND vehicle_id = $2 AND source = 'live'
		     AND tstzrange(started_at, GREATEST(ended_at, started_at)) && tstzrange($3::timestamptz, GREATEST($4::timestamptz, $3::timestamptz)))`,
		s.userID, vid, start.UTC(), end.UTC()).Scan(&ok)
	return ok, err
}

// ChartDrive is the part of a drive the iPhone's charts need: when, how far and the
// charge at each end. No ids, places or coordinates.
type ChartDrive struct {
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	DistanceMi  float64   `json:"distance_mi"`
	StartSoCPct float64   `json:"start_soc_pct"`
	EndSoCPct   float64   `json:"end_soc_pct"`
}

// MaxChartRows bounds one chart answer (newest rows win when a window holds more).
const MaxChartRows = 5000

// ListChartPoints returns the user's drives that started in [since, until), oldest
// first, for the charts: the same rows the list shows (at least a minute, no
// re-parking rows), narrowed to ChartDrive. rivianVehicleID "" means every truck;
// at most MaxChartRows, the newest if there are more.
func (s *Store) ListChartPoints(ctx context.Context, since, until time.Time, rivianVehicleID string) ([]ChartDrive, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.started_at, d.ended_at, COALESCE(d.distance_mi,0), COALESCE(d.start_soc_pct,0), COALESCE(d.end_soc_pct,0)
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1
		  AND d.ended_at - d.started_at >= interval '1 minute'
		  AND d.started_at >= $2 AND d.started_at < $3
		  AND ($4 = '' OR v.rivian_vehicle_id = $4)
		ORDER BY d.started_at DESC
		LIMIT $5`, s.userID, since, until, rivianVehicleID, MaxChartRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChartDrive
	for rows.Next() {
		var c ChartDrive
		if err := rows.Scan(&c.StartedAt, &c.EndedAt, &c.DistanceMi, &c.StartSoCPct, &c.EndSoCPct); err != nil {
			return nil, err
		}
		if IsReparking(Drive{DistanceMi: c.DistanceMi, StartSoCPct: c.StartSoCPct, EndSoCPct: c.EndSoCPct}) {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Oldest first, for charting.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// driveMetadata is what a drive row writes into drives.metadata: only the flag, and only when it is set (an absent key means false).
func driveMetadata(d Drive) map[string]any {
	if d.LocationUnavailable {
		return map[string]any{"location_unavailable": true}
	}
	return map[string]any{}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// locationUnavailableSQL fills Drive.LocationUnavailable.
const locationUnavailableSQL = `COALESCE((d.metadata->>'location_unavailable')::boolean, false)`
