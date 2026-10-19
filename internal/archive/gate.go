package archive

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier is a *sql.DB, *sql.Conn or *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var monthlyPartition = regexp.MustCompile(`^vehicle_state_y([0-9]{4})_([0-9]{2})$`)

// MonthArchived is the gate sample thinning and retention pass before they
// reduce or drop a month: every (user, vehicle) with rows in partition (a
// monthly vehicle_state partition, attached or not) must have a verified
// archive object for that month which isn't waiting to be re-archived and
// isn't held back after an admin restore (rethin_after). A partition with no
// rows passes.
func MonthArchived(ctx context.Context, q Querier, partition string, now time.Time) (bool, error) {
	month, err := partitionMonth(partition)
	if err != nil {
		return false, err
	}
	var ok bool
	err = q.QueryRowContext(ctx, `
		SELECT NOT EXISTS (
		    SELECT 1 FROM `+pgx.Identifier{partition}.Sanitize()+` s
		     WHERE NOT EXISTS (
		         SELECT 1 FROM archive_months a
		          WHERE a.user_id = s.user_id AND a.vehicle_id = s.vehicle_id AND a.month = $1::date
		            AND a.verified_at IS NOT NULL AND NOT a.needs_rearchive
		            AND (a.rethin_after IS NULL OR a.rethin_after <= $2)))`,
		month, now).Scan(&ok)
	return ok, err
}

// MonthArchivedExact is MonthArchived plus a match of every (user, vehicle) in
// partition against its object: row count, first and last at. Thinning, which
// happens once per month, checks it under its partition lock, so rows that
// reached an archived month without setting needs_rearchive (an importer that
// predates it) refuse the thinning instead of being thinned away.
func MonthArchivedExact(ctx context.Context, q Querier, partition string, now time.Time) (bool, error) {
	month, err := partitionMonth(partition)
	if err != nil {
		return false, err
	}
	var ok bool
	err = q.QueryRowContext(ctx, `
		SELECT NOT EXISTS (
		    SELECT 1
		      FROM (SELECT user_id, vehicle_id, count(*) AS n, min(at) AS first_at, max(at) AS last_at
		              FROM `+pgx.Identifier{partition}.Sanitize()+` GROUP BY user_id, vehicle_id) s
		      LEFT JOIN archive_months a
		        ON a.user_id = s.user_id AND a.vehicle_id = s.vehicle_id AND a.month = $1::date
		       AND a.verified_at IS NOT NULL AND NOT a.needs_rearchive
		       AND (a.rethin_after IS NULL OR a.rethin_after <= $2)
		     WHERE a.user_id IS NULL OR a.rows <> s.n OR a.first_at <> s.first_at OR a.last_at <> s.last_at)`,
		month, now).Scan(&ok)
	return ok, err
}

func partitionMonth(partition string) (time.Time, error) {
	m := monthlyPartition.FindStringSubmatch(partition)
	if m == nil {
		return time.Time{}, fmt.Errorf("archive: %q is not a monthly vehicle_state partition", partition)
	}
	month, err := time.Parse("2006-01", m[1]+"-"+m[2])
	if err != nil {
		return time.Time{}, fmt.Errorf("archive: %q: %w", partition, err)
	}
	return month, nil
}
