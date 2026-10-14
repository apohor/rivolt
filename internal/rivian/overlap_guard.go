package rivian

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgExclusionViolation is SQLSTATE 23P01, raised when a row conflicts
// with an exclusion constraint.
const pgExclusionViolation = "23P01"

// sessionOverlapRejected reports whether err is the database refusing a
// live charge or drive because it overlaps another live session for the
// same vehicle (charges_live_no_overlap / drives_live_no_overlap,
// migration 0041). That only happens when two recorders write the same
// vehicle; the session that got there first is kept.
func sessionOverlapRejected(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == pgExclusionViolation
}
