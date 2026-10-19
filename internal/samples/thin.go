// Sample thinning: a monthly vehicle_state partition whose rows are all older
// than the thinning age is replaced by a copy that keeps, per vehicle, the
// first and last row of every drive and charge, every row where the gear, the
// charging state, the charger status or the power state changed, and the first
// row of each time bucket (30 s driving, 2 min charging, 15 min parked).
// Drive and charge pages of old months keep a coarser chart and route; drives,
// charges and their totals are other tables and are untouched.
//
// One partition per run. The thinned copy is built, verified and swapped in one
// transaction that holds a SHARE lock on the old partition only (writes into
// that month, which only restores and imports make, wait for it), with
// lock_timeout 5s so a busy table means "try tomorrow", never a queue. The full
// partition is kept, detached, as <name>_full for FullKeep for undo, then
// dropped. sample_tiers (migration 0072) records each month.

package samples

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
)

// MinThinAfterDays is the youngest data thinning may touch: recent detail is
// what repairs and plan vs actual look at.
const MinThinAfterDays = 30

// FullKeep is how long a thinned month's full partition stays (detached) so a
// bad thinning can be undone.
const FullKeep = 7 * 24 * time.Hour

// sampleMaintenanceLockID serialises thinning across replicas, and with the
// retention sweep (internal/samples/retention.go uses the same value), so the
// two never work on vehicle_state partitions at the same time.
const sampleMaintenanceLockID int64 = 0x72765f72657465

// thinPartitionName is the partition helper's naming scheme (migration 0007).
var thinPartitionName = regexp.MustCompile(`^vehicle_state_y([0-9]{4})_([0-9]{2})$`)

// ErrThinVerify reports a thinned copy that failed its checks; nothing was
// swapped.
var ErrThinVerify = errors.New("thinned copy failed verification")

// ErrNotArchived refuses a thinning or a drop that would lose detail no
// archive holds. Under N19 a month is reduced only after every (user, vehicle)
// in it has a verified archive object (archive.MonthArchived). Dry runs don't
// need one.
var ErrNotArchived = errors.New("month not archived: needs a verified archive first")

// Thinner thins old vehicle_state partitions.
type Thinner struct {
	db        *sql.DB
	afterDays int
	// DryRun computes and reports what a run would keep, and changes nothing.
	DryRun bool
	// Now is the clock (tests set it).
	Now func() time.Time
	// Report, when set, is told each month processed: in a dry run what it
	// would keep, otherwise what it kept.
	Report func(r ThinReport)
	// OnRethin, when set, is told each restored (user, vehicle) month thinned
	// again once its hold (archive_months.rethin_after) ran out.
	OnRethin func(partition string, uid, vid uuid.UUID, deleted int64)
	// OnRepair, when set, is told how many objects were flagged for
	// re-archiving because their month's rows no longer matched them.
	OnRepair func(partition string, flagged int64)
}

// ThinReport is one month's thinning.
type ThinReport struct {
	Partition               string
	RowsBefore, RowsAfter   int64
	BytesBefore, BytesAfter int64
	DryRun                  bool
}

// ParseThinAfterDays reads RIVOLT_SAMPLE_THIN_AFTER_DAYS: "" or "0" never
// thins (the default); otherwise at least MinThinAfterDays.
func ParseThinAfterDays(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("RIVOLT_SAMPLE_THIN_AFTER_DAYS=%q: want a whole number of days (0 never thins)", v)
	}
	if n > 0 && n < MinThinAfterDays {
		return 0, fmt.Errorf("RIVOLT_SAMPLE_THIN_AFTER_DAYS=%d: at least %d days (0 never thins)", n, MinThinAfterDays)
	}
	return n, nil
}

// NewThinner returns the thinning job for afterDays, or nil for 0 (off).
func NewThinner(db *sql.DB, afterDays int) *Thinner {
	if db == nil || afterDays <= 0 {
		return nil
	}
	return &Thinner{db: db, afterDays: afterDays, Now: time.Now}
}

// ThinCandidate picks the month to thin: the oldest month whose partition
// ends at or before now-afterDays and that is not yet thinned. months must be
// sorted oldest first.
func ThinCandidate(months []time.Time, thinned map[time.Time]bool, now time.Time, afterDays int) (time.Time, bool) {
	limit := now.UTC().Add(-time.Duration(afterDays) * 24 * time.Hour)
	for _, m := range months {
		if thinned[m] {
			continue
		}
		if !m.AddDate(0, 1, 0).After(limit) {
			return m, true
		}
	}
	return time.Time{}, false
}

// Run thins at most one month and drops full copies past FullKeep. It returns
// the number of partitions it thinned (0 or 1).
func (t *Thinner) Run(ctx context.Context) (int64, error) {
	if t == nil {
		return 0, nil
	}
	conn, err := t.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, sampleMaintenanceLockID).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, nil
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, sampleMaintenanceLockID) //nolint:errcheck

	now := t.Now().UTC()
	if !t.DryRun {
		if err := t.dropExpiredFull(ctx, conn, now); err != nil {
			return 0, err
		}
		if err := t.rethinDue(ctx, conn, now); err != nil {
			return 0, err
		}
	}
	months, err := attachedMonths(ctx, conn)
	if err != nil {
		return 0, err
	}
	thinned := map[time.Time]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT month FROM sample_tiers`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var m time.Time
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return 0, err
		}
		thinned[time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	month, ok := ThinCandidate(months, thinned, now, t.afterDays)
	if !ok {
		return 0, nil
	}
	if !t.DryRun {
		// Checked again under the partition lock in thin(); this refuses early.
		ok, err := archive.MonthArchived(ctx, conn, monthPartition(month), now)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("%s: %w", monthPartition(month), ErrNotArchived)
		}
	}
	rep, err := t.thin(ctx, conn, month)
	if errors.Is(err, ErrNotArchived) {
		// The exact gate refused: an object lacks rows the month has. Flag
		// those for re-archiving so the next archive run heals the month
		// (DA on #628); thinning retries tomorrow.
		if n, ferr := archive.FlagMismatched(ctx, conn, monthPartition(month)); ferr == nil && n > 0 && t.OnRepair != nil {
			t.OnRepair(monthPartition(month), n)
		}
	}
	if err != nil {
		return 0, err
	}
	if t.Report != nil {
		t.Report(rep)
	}
	if t.DryRun {
		return 0, nil
	}
	return 1, nil
}

// attachedMonths lists the months of vehicle_state's attached monthly
// partitions, oldest first.
func attachedMonths(ctx context.Context, conn *sql.Conn) ([]time.Time, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'vehicle_state'::regclass ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if m, ok := partitionMonth(name); ok {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func partitionMonth(name string) (time.Time, bool) {
	m := thinPartitionName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	if mo < 1 || mo > 12 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC), true
}

func monthPartition(m time.Time) string {
	return fmt.Sprintf("vehicle_state_y%04d_%02d", m.Year(), int(m.Month()))
}

// keepSQL selects the (vehicle_id, at) keys of the rows a thinned month keeps,
// from the partition named by %[1]s.
const keepSQL = `
SELECT vehicle_id, at FROM (
  SELECT vehicle_id, at,
         drive_number, charge_number,
         row_number() OVER (PARTITION BY vehicle_id, drive_number ORDER BY at)       AS d_first,
         row_number() OVER (PARTITION BY vehicle_id, drive_number ORDER BY at DESC)  AS d_last,
         row_number() OVER (PARTITION BY vehicle_id, charge_number ORDER BY at)      AS c_first,
         row_number() OVER (PARTITION BY vehicle_id, charge_number ORDER BY at DESC) AS c_last,
         (shift_state    IS DISTINCT FROM lag(shift_state)    OVER v
       OR charging_state IS DISTINCT FROM lag(charging_state) OVER v
       OR charger_status IS DISTINCT FROM lag(charger_status) OVER v
       OR power_state    IS DISTINCT FROM lag(power_state)    OVER v) AS changed,
         row_number() OVER (PARTITION BY vehicle_id, bucket_s, floor(extract(epoch FROM at) / bucket_s) ORDER BY at) AS b_first
    FROM (SELECT *,
                 CASE WHEN shift_state IN ('D', 'R') OR speed_mph > 1 THEN 30
                      WHEN charging_state ILIKE '%%active%%' OR charger_power_kw > 0.5 THEN 120
                      ELSE 900 END AS bucket_s
            FROM %[1]s) s
  WINDOW v AS (PARTITION BY vehicle_id ORDER BY at)
) k
WHERE changed OR b_first = 1
   OR (drive_number IS NOT NULL AND (d_first = 1 OR d_last = 1))
   OR (charge_number IS NOT NULL AND (c_first = 1 OR c_last = 1))`

// thin builds, verifies and (unless DryRun) swaps one month's thinned copy.
func (t *Thinner) thin(ctx context.Context, conn *sql.Conn, month time.Time) (ThinReport, error) {
	name := monthPartition(month)
	old, thin, full := `"`+name+`"`, `"`+name+`_thin"`, `"`+name+`_full"`
	rep := ThinReport{Partition: name, DryRun: t.DryRun}
	if err := conn.QueryRowContext(ctx, `SELECT count(*), pg_total_relation_size($1::regclass) FROM `+old, name).Scan(&rep.RowsBefore, &rep.BytesBefore); err != nil {
		return rep, err
	}
	if t.DryRun {
		err := conn.QueryRowContext(ctx, `SELECT count(*) FROM (`+fmt.Sprintf(keepSQL, old)+`) k`).Scan(&rep.RowsAfter)
		return rep, err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer tx.Rollback() //nolint:errcheck
	exec := func(q string, args ...any) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}
	from, to := month, month.AddDate(0, 1, 0)
	steps := []struct {
		what string
		q    string
	}{
		{"lock timeout", `SET LOCAL lock_timeout = '5s'`},
		// Writes into this month wait; the live month and reads don't.
		{"lock", `LOCK TABLE ` + old + ` IN SHARE MODE`},
		{"create", `CREATE TABLE ` + thin + ` (LIKE vehicle_state INCLUDING ALL)`},
		// The partition's range as a constraint lets ATTACH skip its scan.
		{"range", fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s CHECK (at >= '%s' AND at < '%s')`, thin, `"`+name+`_range"`, from.Format(time.RFC3339), to.Format(time.RFC3339))},
		{"copy", `INSERT INTO ` + thin + ` SELECT o.* FROM ` + old + ` o JOIN (` + fmt.Sprintf(keepSQL, old) + `) k USING (vehicle_id, at)`},
	}
	for _, s := range steps {
		if err := exec(s.q); err != nil {
			return rep, fmt.Errorf("%s %s: %w", s.what, name, err)
		}
		if s.what == "lock" {
			// Under the SHARE lock no row can arrive, so this is the check that
			// counts: every (user, vehicle) in the month is archived, verified,
			// and still holds exactly the rows its object holds.
			ok, err := archive.MonthArchivedExact(ctx, tx, name, t.Now().UTC())
			if err != nil {
				return rep, err
			}
			if !ok {
				return rep, fmt.Errorf("%s: %w", name, ErrNotArchived)
			}
		}
	}
	if err := verifyThin(ctx, tx, old, thin); err != nil {
		return rep, fmt.Errorf("%s: %w", name, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*), pg_total_relation_size($1::regclass) FROM `+thin, name+"_thin").Scan(&rep.RowsAfter, &rep.BytesAfter); err != nil {
		return rep, err
	}
	swap := []struct {
		what string
		q    string
	}{
		{"detach", `ALTER TABLE vehicle_state DETACH PARTITION ` + old},
		{"attach", fmt.Sprintf(`ALTER TABLE vehicle_state ATTACH PARTITION %s FOR VALUES FROM ('%s') TO ('%s')`, thin, from.Format(time.RFC3339), to.Format(time.RFC3339))},
		{"rename full", `ALTER TABLE ` + old + ` RENAME TO ` + full},
		{"rename thin", `ALTER TABLE ` + thin + ` RENAME TO ` + old},
	}
	for _, s := range swap {
		if err := exec(s.q); err != nil {
			return rep, fmt.Errorf("%s %s: %w", s.what, name, err)
		}
	}
	if err := exec(`INSERT INTO sample_tiers (month, rows_before, rows_after, bytes_before, bytes_after) VALUES ($1, $2, $3, $4, $5)`,
		month, rep.RowsBefore, rep.RowsAfter, rep.BytesBefore, rep.BytesAfter); err != nil {
		return rep, err
	}
	return rep, tx.Commit()
}

// verifyThin checks a thinned copy before it replaces the original: no more
// rows than before, every vehicle still has rows, and every drive and charge
// keeps its first and last row.
func verifyThin(ctx context.Context, tx *sql.Tx, old, thin string) error {
	var before, after, lostVehicles, lostEnds int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM `+old+`), (SELECT count(*) FROM `+thin+`)`).Scan(&before, &after); err != nil {
		return err
	}
	if after > before || (before > 0 && after == 0) {
		return fmt.Errorf("%w: %d rows before, %d after", ErrThinVerify, before, after)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM (SELECT DISTINCT vehicle_id FROM `+old+`) v
		 WHERE NOT EXISTS (SELECT 1 FROM `+thin+` t WHERE t.vehicle_id = v.vehicle_id)`).Scan(&lostVehicles); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `
		WITH ends AS (
		  SELECT vehicle_id, min(at) AS a, max(at) AS b FROM `+old+` WHERE drive_number IS NOT NULL GROUP BY vehicle_id, drive_number
		  UNION ALL
		  SELECT vehicle_id, min(at), max(at) FROM `+old+` WHERE charge_number IS NOT NULL GROUP BY vehicle_id, charge_number)
		SELECT count(*) FROM ends e
		 WHERE NOT EXISTS (SELECT 1 FROM `+thin+` t WHERE t.vehicle_id = e.vehicle_id AND t.at = e.a)
		    OR NOT EXISTS (SELECT 1 FROM `+thin+` t WHERE t.vehicle_id = e.vehicle_id AND t.at = e.b)`).Scan(&lostEnds); err != nil {
		return err
	}
	if lostVehicles > 0 || lostEnds > 0 {
		return fmt.Errorf("%w: %d vehicles and %d drive/charge ends missing", ErrThinVerify, lostVehicles, lostEnds)
	}
	return nil
}

// dropExpiredFull drops the full copies kept for undo once FullKeep has passed.
func (t *Thinner) dropExpiredFull(ctx context.Context, conn *sql.Conn, now time.Time) error {
	rows, err := conn.QueryContext(ctx, `SELECT month FROM sample_tiers WHERE full_dropped_at IS NULL AND thinned_at < $1`, now.Add(-FullKeep))
	if err != nil {
		return err
	}
	var months []time.Time
	for rows.Next() {
		var m time.Time
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return err
		}
		months = append(months, time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range months {
		full := `"` + monthPartition(m) + `_full"`
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS `+full); err != nil {
			return fmt.Errorf("drop %s: %w", full, err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE sample_tiers SET full_dropped_at = $2 WHERE month = $1`, m, now); err != nil {
			return err
		}
	}
	return nil
}

// rethinDue thins again the restored (user, vehicle) months whose hold ran
// out (archive_months.rethin_after), one vehicle at a time and only that
// vehicle's rows: the rest of the month is thinned already. Each needs its
// object to hold exactly the vehicle's rows (verified, not flagged); one that
// doesn't is flagged for re-archiving and retried on a later run.
func (t *Thinner) rethinDue(ctx context.Context, conn *sql.Conn, now time.Time) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT user_id, vehicle_id, month FROM archive_months
		 WHERE rethin_after IS NOT NULL AND rethin_after <= $1 ORDER BY month LIMIT 20`, now)
	if err != nil {
		return err
	}
	type due struct {
		uid, vid uuid.UUID
		month    time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.uid, &d.vid, &d.month); err != nil {
			rows.Close()
			return err
		}
		d.month = time.Date(d.month.Year(), d.month.Month(), 1, 0, 0, 0, 0, time.UTC)
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		if err := t.rethinPair(ctx, conn, d.uid, d.vid, d.month); err != nil {
			return fmt.Errorf("rethin %s: %w", monthPartition(d.month), err)
		}
	}
	return nil
}

func (t *Thinner) rethinPair(ctx context.Context, conn *sql.Conn, uid, vid uuid.UUID, month time.Time) error {
	name := monthPartition(month)
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return err
	}
	clear := `UPDATE archive_months SET rethin_after = NULL WHERE user_id = $1 AND vehicle_id = $2 AND month = $3::date`
	if !exists {
		_, err := conn.ExecContext(ctx, clear, uid, vid, month)
		return err
	}
	q := `"` + name + `"`
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return err
	}
	// No row reaches the old month while this vehicle's rows are checked and
	// reduced.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE `+q+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	ok, mismatch, err := archive.PairArchivedExact(ctx, tx, name, uid, vid)
	if err != nil {
		return err
	}
	if !ok {
		if mismatch {
			if err := archive.MarkRearchive(ctx, tx, uid, vid, month); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			if t.OnRepair != nil {
				t.OnRepair(name, 1)
			}
		}
		return nil // flagged or not verified: after the next archive run
	}
	// uid and vid are uuid.UUID strings: safe as literals in the relation.
	pair := fmt.Sprintf(`(SELECT * FROM %s WHERE user_id = '%s' AND vehicle_id = '%s') pair`, q, uid, vid)
	res, err := tx.ExecContext(ctx, `
		DELETE FROM `+q+` v
		 WHERE v.user_id = $1 AND v.vehicle_id = $2
		   AND NOT EXISTS (SELECT 1 FROM (`+fmt.Sprintf(keepSQL, pair)+`) k WHERE k.vehicle_id = v.vehicle_id AND k.at = v.at)`, uid, vid)
	if err != nil {
		return err
	}
	deleted, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, clear, uid, vid, month); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if t.OnRethin != nil {
		t.OnRethin(name, uid, vid, deleted)
	}
	return nil
}
