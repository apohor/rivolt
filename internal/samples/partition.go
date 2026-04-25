// Package samples: partition maintenance helpers.
//
// The `vehicle_state` table is RANGE-partitioned by month
// (see migration 0007). A write into a timestamp range with no
// matching partition raises
//
//	ERROR: no partition of relation "vehicle_state" found for row
//
// which would stall the live recorder the moment the clock rolls
// past the last partition created at migration time. Preventing
// that is the janitor's only job.
//
// We don't use pg_partman: one PL/pgSQL function + one goroutine
// here is simpler to ship and easier for self-hosters to audit
// than an extension dependency. If partition management ever grows
// a retention story, graduate to pg_partman in phase 3.

package samples

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// PartitionJanitor keeps `vehicle_state` ahead of the clock by at
// least `lookaheadMonths` partitions. It does this by calling the
// `rivolt_ensure_vehicle_state_partition(ts)` SQL helper once per
// target month.
type PartitionJanitor struct {
	db              *sql.DB
	lookaheadMonths int
	interval        time.Duration
}

// NewPartitionJanitor wires a janitor with sensible defaults. 3
// months of lookahead is enough that a pod that runs continuously
// for a month still never writes into an unpartitioned range, and
// a pod that restarts hourly (deploys, node drains) has slack for
// clock skew. The interval is also 1 hour — cheap, boring, and
// avoids the "I deployed at 23:59 UTC on the last of the month
// and the janitor fired at 23:30" edge.
func NewPartitionJanitor(db *sql.DB) *PartitionJanitor {
	return &PartitionJanitor{
		db:              db,
		lookaheadMonths: 3,
		interval:        1 * time.Hour,
	}
}

// EnsureLookahead creates the current partition plus
// `lookaheadMonths` future partitions if they don't already exist.
// Idempotent; safe to call repeatedly. Returns the first error
// encountered but always attempts every month.
func (j *PartitionJanitor) EnsureLookahead(ctx context.Context) error {
	if j == nil || j.db == nil {
		return nil
	}
	var firstErr error
	now := time.Now().UTC()
	for i := 0; i <= j.lookaheadMonths; i++ {
		// AddDate with days=0 naturally wraps into the next
		// month at month boundaries; the SQL helper date_truncs
		// the value so mid-month clocks land on month_start.
		target := now.AddDate(0, i, 0)
		var name string
		err := j.db.QueryRowContext(ctx,
			`SELECT rivolt_ensure_vehicle_state_partition($1)`, target,
		).Scan(&name)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ensure partition for %s: %w",
				target.Format("2006-01"), err)
		}
	}
	return firstErr
}

// Run blocks until ctx is cancelled, calling EnsureLookahead on an
// `interval` ticker. Intended to be launched as a dedicated
// goroutine at startup. Errors are logged, never returned — the
// next tick will retry.
func (j *PartitionJanitor) Run(ctx context.Context) {
	if j == nil {
		return
	}
	// One eager run on start so we don't wait a full tick for
	// the first ensure. The migration already creates 6 months
	// of partitions, so this is belt-and-suspenders on the
	// "I deployed Jul 1 and the migration ran Dec 15 last year"
	// case.
	if err := j.EnsureLookahead(ctx); err != nil {
		slog.Warn("samples partition janitor: initial ensure failed",
			"err", err.Error())
	}
	t := time.NewTicker(j.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := j.EnsureLookahead(ctx); err != nil {
				slog.Warn("samples partition janitor: ensure failed",
					"err", err.Error())
			}
		}
	}
}
