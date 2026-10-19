package archive

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Execer is a *sql.Tx, *sql.Conn or *sql.DB.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// MarkRearchive flags (uid, vehicleID, month) for re-archiving: rows were
// added to a month that may already be archived. Writers call it in the
// transaction that inserted the rows (samples.Store does, for every insert
// path). It is a no-op for a month without an archive row, which the next
// archive run picks up anyway. The object stays readable meanwhile: readers
// take it plus the database rows it lacks.
func MarkRearchive(ctx context.Context, ex Execer, uid, vehicleID uuid.UUID, month time.Time) error {
	m := month.UTC()
	_, err := ex.ExecContext(ctx, `
		UPDATE archive_months SET needs_rearchive = TRUE, rearchive_gen = rearchive_gen + 1
		 WHERE user_id = $1 AND vehicle_id = $2 AND month = $3::date`,
		uid, vehicleID, time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC))
	return err
}
