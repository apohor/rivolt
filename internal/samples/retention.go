// Sample retention: whole monthly vehicle_state partitions are dropped once
// every row in them is older than the retention period. Drives, charges and
// everything derived from them are separate tables and are kept; what goes is
// the raw telemetry behind old drive and charge charts.

package samples

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MinRetentionMonths is the shortest retention accepted: anything shorter is
// almost certainly a typo, and dropping last month's samples by mistake can't
// be undone.
const MinRetentionMonths = 3

// retentionLockID serialises the sweep across replicas (pg_try_advisory_lock).
const retentionLockID int64 = 0x72765f72657465 // "rv_rete"

// partitionName is the helper's naming scheme (migration 0007):
// vehicle_state_yYYYY_MM, the month the partition starts (UTC).
var partitionName = regexp.MustCompile(`^vehicle_state_y([0-9]{4})_([0-9]{2})$`)

// Retention drops vehicle_state partitions past the retention period.
type Retention struct {
	db     *sql.DB
	months int
	now    func() time.Time
	// Dropped, when set, is told each partition dropped with its row count
	// estimate (pg_class.reltuples): the caller logs and counts it.
	Dropped func(name string, estRows int64)
}

// ParseRetentionMonths reads RIVOLT_SAMPLE_RETENTION_MONTHS: "" or "0" keeps
// samples forever (the default, so a self-hosted install keeps its history
// unless it opts in); otherwise at least MinRetentionMonths.
func ParseRetentionMonths(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("RIVOLT_SAMPLE_RETENTION_MONTHS=%q: want a whole number of months (0 keeps samples forever)", v)
	}
	if n > 0 && n < MinRetentionMonths {
		return 0, fmt.Errorf("RIVOLT_SAMPLE_RETENTION_MONTHS=%d: at least %d months (0 keeps samples forever)", n, MinRetentionMonths)
	}
	return n, nil
}

// NewRetention returns the sweep for months of retention, or nil for 0
// (keep forever).
func NewRetention(db *sql.DB, months int) *Retention {
	if db == nil || months <= 0 {
		return nil
	}
	return &Retention{db: db, months: months, now: time.Now}
}

// RetentionCutoff is the first month whose partition is kept: partitions for
// months before it hold only rows older than months months, and go. Samples
// therefore live at least months months and less than months+1.
func RetentionCutoff(now time.Time, months int) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -months, 0)
}

type partition struct {
	name     string
	month    time.Time
	attached bool
	pending  bool  // DETACH ... CONCURRENTLY started but not finished
	estRows  int64 // pg_class.reltuples (-1 before the first ANALYZE)
}

// Run drops every partition before the cutoff, oldest first, and reports how
// many it dropped. One replica sweeps at a time; the others return 0. It stops
// at the first error; the rest wait for the next day's sweep.
func (r *Retention) Run(ctx context.Context) (int64, error) {
	if r == nil {
		return 0, nil
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, retentionLockID).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, nil
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, retentionLockID) //nolint:errcheck

	// DETACH ... CONCURRENTLY refuses to run while the table has a DEFAULT
	// partition; the schema has none, so finding one means someone added it.
	var hasDefault bool
	if err := conn.QueryRowContext(ctx,
		`SELECT partdefid <> 0 FROM pg_partitioned_table WHERE partrelid = 'vehicle_state'::regclass`).Scan(&hasDefault); err != nil {
		return 0, err
	}
	if hasDefault {
		return 0, fmt.Errorf("vehicle_state has a DEFAULT partition: retention needs DETACH ... CONCURRENTLY, which it forbids")
	}
	parts, err := listPartitions(ctx, conn)
	if err != nil {
		return 0, err
	}
	cutoff := RetentionCutoff(r.now(), r.months)
	// Never the current month or later, whatever the cutoff says.
	cur := RetentionCutoff(r.now(), 0)
	// A lock that isn't granted in 5 s is given up rather than queued behind,
	// as the partition helper does (migration 0070).
	if _, err := conn.ExecContext(ctx, `SET lock_timeout = '5s'`); err != nil {
		return 0, err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `RESET lock_timeout`) //nolint:errcheck
	var dropped int64
	for _, p := range parts {
		if !p.month.Before(cutoff) || !p.month.Before(cur) {
			continue
		}
		// Names come from the catalog and match partitionName, so quoting is
		// plain double quotes.
		q := `"` + p.name + `"`
		if p.attached {
			// CONCURRENTLY takes SHARE UPDATE EXCLUSIVE on vehicle_state, so the
			// recorder keeps writing; it can't run in a transaction block, and
			// database/sql runs each Exec on its own here.
			stmt := `ALTER TABLE vehicle_state DETACH PARTITION ` + q + ` CONCURRENTLY`
			if p.pending {
				stmt = `ALTER TABLE vehicle_state DETACH PARTITION ` + q + ` FINALIZE`
			}
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return dropped, fmt.Errorf("detach %s: %w", p.name, err)
			}
		}
		// Only after a successful detach (an error above returns first).
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS `+q); err != nil {
			return dropped, fmt.Errorf("drop %s: %w", p.name, err)
		}
		dropped++
		if r.Dropped != nil {
			r.Dropped(p.name, p.estRows)
		}
	}
	return dropped, nil
}

// listPartitions returns vehicle_state's monthly partitions, attached or left
// detached by an interrupted sweep, oldest first.
func listPartitions(ctx context.Context, conn *sql.Conn) ([]partition, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT c.relname, i.inhrelid IS NOT NULL, COALESCE(i.inhdetachpending, false), c.reltuples::bigint
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = current_schema()
		  LEFT JOIN pg_inherits i ON i.inhrelid = c.oid AND i.inhparent = 'vehicle_state'::regclass
		 WHERE c.relkind = 'r' AND c.relname ~ '^vehicle_state_y[0-9]{4}_[0-9]{2}$'
		 ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []partition
	for rows.Next() {
		var p partition
		if err := rows.Scan(&p.name, &p.attached, &p.pending, &p.estRows); err != nil {
			return nil, err
		}
		m := partitionName.FindStringSubmatch(p.name)
		if m == nil {
			continue
		}
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if mo < 1 || mo > 12 {
			continue
		}
		p.month = time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
		out = append(out, p)
	}
	return out, rows.Err()
}
