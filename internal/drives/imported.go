package drives

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/apohor/rivolt/internal/db"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/histfloor"
	"time"
)

// ErrImportClosed: the batch is undone (or not this user's), so nothing more may be written under it.
var ErrImportClosed = errors.New("drives: import batch is closed")

// lockLiveBatch takes a KEY SHARE lock on the live batch row. Undo's FOR UPDATE on the row waits for the insert's transaction, and an
// insert that starts after Undo committed finds undone_at set, so no row can be written under a batch that was already undone.
func lockLiveBatch(ctx context.Context, tx *sql.Tx, userID, importID uuid.UUID) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM imports WHERE id = $1 AND user_id = $2 AND undone_at IS NULL FOR KEY SHARE`, importID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrImportClosed
	}
	return err
}

// InsertImported writes one drive that came from a history import (batch importID, an imports row). It is insert-only: a drive with the
// same (vehicle, external id) is left exactly as it is, so a re-import never overwrites a row the user has edited since (notes, tags,
// trailer override), which Upsert would. Reports whether a row was written; false means it already existed, or started before the
// user's history floor and the ctx does not carry histfloor.Bypass (the importer's does). extra is merged into the row's metadata.
func (s *Store) InsertImported(ctx context.Context, importID uuid.UUID, d Drive, extra map[string]any) (bool, error) {
	if importID == uuid.Nil {
		return false, fmt.Errorf("drives: InsertImported needs an import id")
	}
	if below, err := histfloor.Below(ctx, s.db, s.userID, d.StartedAt); err != nil {
		return false, err
	} else if below {
		return false, nil
	}
	vid, err := s.vehicles.Resolve(ctx, d.VehicleID)
	if err != nil {
		return false, err
	}
	meta := driveMetadata(d)
	for k, v := range extra {
		meta[k] = v
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockLiveBatch(ctx, tx, s.userID, importID); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO drives (`+driveColumns+`, import_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22::jsonb,$23)
		ON CONFLICT (vehicle_id, external_id) DO NOTHING`,
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
		d.Trailer, d.TrailerOverride, mustJSON(meta), importID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

// Covers reports whether a drive of any source for this vehicle already covers the session [start, end): the two overlap by at least
// half of the shorter one, or they start within five minutes of each other and overlap at all. It stops two apps' exports (or an
// export and the recorder) from counting one trip twice, while a short drive that does not overlap anything is kept. A session of no
// length overlaps nothing, as in LiveCovers.
func (s *Store) Covers(ctx context.Context, rivianVehicleID string, start, end time.Time) (bool, error) {
	got, err := s.CoversBatch(ctx, rivianVehicleID, []time.Time{start}, []time.Time{end})
	if err != nil {
		return false, err
	}
	return got[0], nil
}

// CoversBatch is Covers for many sessions at once: one query per call, not per session. starts and ends are parallel; the answer has one
// entry per session. Callers send chunks of about 1000.
func (s *Store) CoversBatch(ctx context.Context, rivianVehicleID string, starts, ends []time.Time) ([]bool, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.i - 1 FROM unnest(string_to_array($3, ',')::timestamptz[], string_to_array($4, ',')::timestamptz[]) WITH ORDINALITY AS n(s, e, i)
		 WHERE EXISTS (
		  SELECT 1 FROM drives d,
		         LATERAL (SELECT GREATEST(d.ended_at, d.started_at) AS e_end, GREATEST(n.e, n.s) AS n_end) x
		   WHERE d.user_id = $1 AND d.vehicle_id = $2
		     AND d.started_at <= GREATEST(n.e, n.s)
		     AND d.started_at >= n.s - interval '7 days'
		     AND tstzrange(d.started_at, x.e_end) && tstzrange(n.s, x.n_end)
		     AND (LEAST(x.e_end, x.n_end) - GREATEST(d.started_at, n.s)
		            >= 0.5 * LEAST(x.e_end - d.started_at, x.n_end - n.s)
		          OR abs(extract(epoch FROM d.started_at - n.s)) <= 300))`,
		s.userID, vid, db.TimeList(starts), db.TimeList(ends))
	if err != nil {
		return nil, err
	}
	return flags(rows, len(starts))
}

// UndoImport deletes the drives a batch brought in, with the per-drive rows that hang off them (see Reset), inside tx (the caller's, so the
// batch row and the charges go in the same transaction). Scoped by user as well as batch. Returns the number of drives deleted.
func UndoImport(ctx context.Context, tx *sql.Tx, userID, importID uuid.UUID) (int64, error) {
	for _, t := range perDriveTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE user_id = $1 AND drive_id IN
			(SELECT external_id FROM drives WHERE user_id = $1 AND import_id = $2)`, userID, importID); err != nil {
			return 0, fmt.Errorf("undo import, %s: %w", t, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM drives WHERE user_id = $1 AND import_id = $2`, userID, importID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
