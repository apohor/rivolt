package entitlements

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Stats is the launch dashboard's numbers, counts only: the entitlement rows by source and status and the accounts, the demo account left out
// of both. No user id, email or any label that names a person.
type Stats struct {
	ByKind   map[[2]string]int // [source, status] -> rows
	Accounts int
}

// knownSources and knownStatuses are the (source, status) pairs a row can have. A successful read reports every pair, 0 for the ones with no
// row, so an empty table still exports a series per pair: a dashboard or alert can tell "no founding grants" (0) from "the read never
// succeeded" (no series at all).
var (
	knownSources  = []string{SourceStripe, SourceApple, SourceFounding, SourceGrant, SourceTrial}
	knownStatuses = []string{StatusActive, StatusPastDue, StatusEnded}
)

// fillKnown sets every known (source, status) pair that has no count to 0.
func fillKnown(m map[[2]string]int) {
	for _, src := range knownSources {
		for _, st := range knownStatuses {
			if _, ok := m[[2]string{src, st}]; !ok {
				m[[2]string{src, st}] = 0
			}
		}
	}
}

// ReadStats reads the counts (two read-only queries).
func ReadStats(ctx context.Context, db *sql.DB, demo uuid.UUID) (Stats, error) {
	st := Stats{ByKind: map[[2]string]int{}}
	rows, err := db.QueryContext(ctx, `SELECT source, status, count(*) FROM entitlements WHERE user_id <> $1 GROUP BY source, status`, demo)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var src, status string
		var n int
		if err := rows.Scan(&src, &status, &n); err != nil {
			return st, err
		}
		st.ByKind[[2]string{src, status}] = n
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	fillKnown(st.ByKind)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id <> $1`, demo).Scan(&st.Accounts); err != nil {
		return st, err
	}
	return st, nil
}

// RunStats reads the counts every interval until ctx ends and hands them to report (the first read is at once). A failed read is skipped:
// the gauges keep their last values and the next tick tries again.
func RunStats(ctx context.Context, db *sql.DB, demo uuid.UUID, every time.Duration, report func(Stats)) {
	if db == nil || report == nil {
		return
	}
	read := func() {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if st, err := ReadStats(rctx, db, demo); err == nil {
			report(st)
		}
	}
	read()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			read()
		}
	}
}
