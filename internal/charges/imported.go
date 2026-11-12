package charges

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/histfloor"
)

// InsertImported writes one charge that came from a history import (batch importID, an imports row). It is insert-only: a charge with the
// same (vehicle, external id) is left exactly as it is, so a re-import never overwrites a row the user has edited since (price, place,
// notes), which Upsert would. Reports whether a row was written; false means it already existed, or started before the user's history
// floor and the ctx does not carry histfloor.Bypass (the importer's does). extra is merged into the row's metadata (energy_basis and the like).
func (s *Store) InsertImported(ctx context.Context, importID uuid.UUID, c Charge, extra map[string]any) (bool, error) {
	if importID == uuid.Nil {
		return false, fmt.Errorf("charges: InsertImported needs an import id")
	}
	if below, err := histfloor.Below(ctx, s.db, s.userID, c.StartedAt); err != nil {
		return false, err
	} else if below {
		return false, nil
	}
	vid, err := s.vehicles.Resolve(ctx, c.VehicleID)
	if err != nil {
		return false, err
	}
	meta := map[string]any{}
	if err := json.Unmarshal([]byte(chargeMetaDoc(c)), &meta); err != nil {
		return false, err
	}
	for k, v := range extra {
		meta[k] = v
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return false, err
	}
	args := append(s.chargeArgs(vid, c), string(mb), importID)
	ph := make([]string, len(args))
	for i := range ph {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	ph[len(chargeColumns)] = fmt.Sprintf("$%d::jsonb", len(chargeColumns)+1)
	// rivian_charger is bound as a boolean, as in plainChargeUpsert.
	ph[26] = "$27::boolean"
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO charges (`+strings.Join(chargeColumns, ", ")+`, metadata, import_id
		) VALUES (`+strings.Join(ph, ",")+`)
		ON CONFLICT (vehicle_id, external_id) DO NOTHING`, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Covers reports whether a charge of any source for this vehicle already covers the session [start, end): the two overlap by at least
// half of the shorter one, or they start within five minutes of each other and overlap at all. See drives.Store.Covers.
func (s *Store) Covers(ctx context.Context, rivianVehicleID string, start, end time.Time) (bool, error) {
	vid, err := s.vehicles.Resolve(ctx, rivianVehicleID)
	if err != nil {
		return false, err
	}
	var ok bool
	err = s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM charges c,
		         LATERAL (SELECT GREATEST(c.ended_at, c.started_at) AS e_end, GREATEST($4::timestamptz, $3::timestamptz) AS n_end) x
		   WHERE c.user_id = $1 AND c.vehicle_id = $2
		     AND tstzrange(c.started_at, x.e_end) && tstzrange($3::timestamptz, x.n_end)
		     AND (LEAST(x.e_end, x.n_end) - GREATEST(c.started_at, $3::timestamptz)
		            >= 0.5 * LEAST(x.e_end - c.started_at, x.n_end - $3::timestamptz)
		          OR abs(extract(epoch FROM c.started_at - $3::timestamptz)) <= 300))`,
		s.userID, vid, start.UTC(), end.UTC()).Scan(&ok)
	return ok, err
}

// UndoImport deletes the charges a batch brought in, inside tx (the caller's, so the batch row and the drives go in the same
// transaction). Scoped by user as well as batch. rivian_receipts.charge_id goes to NULL by its foreign key. Returns the number deleted.
func UndoImport(ctx context.Context, tx *sql.Tx, userID, importID uuid.UUID) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM charges WHERE user_id = $1 AND import_id = $2`, userID, importID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
