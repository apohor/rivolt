package leases

import (
	"context"
	"database/sql"
	"log/slog"
)

// VehicleSourceFunc yields a snapshot of vehicle IDs for one
// underlying source. Errors are surfaced to the composer, which
// logs and falls through to the remaining sources rather than
// failing the whole reconcile.
type VehicleSourceFunc func(ctx context.Context) ([]string, error)

// NewVehicleSource composes one in-memory monitor source with any
// number of DB-backed sources into a single coordinator
// vehicle-set closure. The returned function dedupes results
// across sources, drops empty strings, and tolerates errors from
// individual sources (logged at debug, skipped — the next tick
// will retry).
//
// `monitor` is the in-process StateMonitor's known set, called
// fresh on every reconcile so newly-logged-in users on this pod
// show up without a restart. Pass nil to skip.
//
// `dbSources` are typically built with QueryStringColumn against
// the vehicles and subscription_leases tables; that combination
// gives true N>1 awareness while still booting a single pod with
// an empty database.
//
// Including monitor here is non-negotiable — `vehicles` only gets
// rows once the recorder writes a sample, and the recorder only
// runs once a subscription is up. Without monitor, you have a
// chicken-and-egg that silently records nothing. (See the v0.16.1
// regression.)
func NewVehicleSource(
	monitor func() []string,
	logger *slog.Logger,
	dbSources ...VehicleSourceFunc,
) func(ctx context.Context) ([]string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context) ([]string, error) {
		seen := make(map[string]struct{})
		if monitor != nil {
			for _, v := range monitor() {
				if v != "" {
					seen[v] = struct{}{}
				}
			}
		}
		for _, src := range dbSources {
			ids, err := src(ctx)
			if err != nil {
				logger.Debug("vehicle source", "err", err.Error())
				continue
			}
			for _, v := range ids {
				if v != "" {
					seen[v] = struct{}{}
				}
			}
		}
		out := make([]string, 0, len(seen))
		for v := range seen {
			out = append(out, v)
		}
		return out, nil
	}
}

// QueryStringColumn runs a one-column SELECT and returns every
// non-empty value. Helper for building DB-backed VehicleSourceFunc
// closures in main.go without duplicating the boilerplate.
func QueryStringColumn(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
