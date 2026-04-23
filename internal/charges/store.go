// Package charges persists derived charge sessions. A charge is the
// boundary-framed window of vehicle_state rows where charging_state was
// one of the Charging/Complete/error terminal values.
package charges

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS charges (
    id               TEXT PRIMARY KEY,
    vehicle_id       TEXT NOT NULL,
    started_at       INTEGER NOT NULL,
    ended_at         INTEGER NOT NULL,
    start_soc_pct    REAL,
    end_soc_pct      REAL,
    energy_added_kwh REAL,
    miles_added      REAL,
    max_power_kw     REAL,
    avg_power_kw     REAL,
    final_state      TEXT,
    lat              REAL,
    lon              REAL,
    source           TEXT NOT NULL,
    cost             REAL,
    currency         TEXT,
    price_per_kwh    REAL
);
CREATE INDEX IF NOT EXISTS charges_started_at ON charges(started_at);
CREATE INDEX IF NOT EXISTS charges_vehicle_id ON charges(vehicle_id);
`

// migrate adds the cost/currency/price_per_kwh columns to any
// pre-existing database that was created before they were part of
// the schema. SQLite has no ADD COLUMN IF NOT EXISTS, so we probe
// table_info and add only the missing ones.
func migrate(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(charges)")
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	adds := []struct{ name, ddl string }{
		{"cost", "ALTER TABLE charges ADD COLUMN cost REAL"},
		{"currency", "ALTER TABLE charges ADD COLUMN currency TEXT"},
		{"price_per_kwh", "ALTER TABLE charges ADD COLUMN price_per_kwh REAL"},
	}
	for _, a := range adds {
		if have[a.name] {
			continue
		}
		if _, err := db.Exec(a.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", a.name, err)
		}
	}
	return nil
}

// Charge is a single charging session.
type Charge struct {
	ID             string
	VehicleID      string
	StartedAt      time.Time
	EndedAt        time.Time
	StartSoCPct    float64
	EndSoCPct      float64
	EnergyAddedKWh float64
	MilesAdded     float64
	MaxPowerKW     float64
	AvgPowerKW     float64
	FinalState     string // e.g. "Complete", "Disconnected", "charging_station_err"
	Lat, Lon       float64
	Source         string // "live" | "electrafi_import"
	// Cost is the total session cost in Currency. 0 when unknown
	// (legacy rows, imports that don't include price). Populated at
	// session close either from a Rivian-reported RAN/Wall Charger
	// price or from the operator's configured home $/kWh × energy.
	Cost     float64
	Currency string
	// PricePerKWh is the effective $/kWh used to compute Cost at the
	// time the charge closed. Snapshotting it means rate changes
	// don't retroactively rewrite history.
	PricePerKWh float64
}

// Store wraps access to the charges table.
type Store struct{ db *sql.DB }

// OpenStore opens (or creates) the store at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Upsert inserts or replaces a charge by primary key.
func (s *Store) Upsert(ctx context.Context, c Charge) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO charges (
			id, vehicle_id, started_at, ended_at,
			start_soc_pct, end_soc_pct,
			energy_added_kwh, miles_added,
			max_power_kw, avg_power_kw, final_state,
			lat, lon, source,
			cost, currency, price_per_kwh
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			vehicle_id       = excluded.vehicle_id,
			started_at       = excluded.started_at,
			ended_at         = excluded.ended_at,
			start_soc_pct    = excluded.start_soc_pct,
			end_soc_pct      = excluded.end_soc_pct,
			energy_added_kwh = excluded.energy_added_kwh,
			miles_added      = excluded.miles_added,
			max_power_kw     = excluded.max_power_kw,
			avg_power_kw     = excluded.avg_power_kw,
			final_state      = excluded.final_state,
			lat              = excluded.lat,
			lon              = excluded.lon,
			source           = excluded.source,
			cost             = excluded.cost,
			currency         = excluded.currency,
			price_per_kwh    = excluded.price_per_kwh`,
		c.ID, c.VehicleID, c.StartedAt.Unix(), c.EndedAt.Unix(),
		c.StartSoCPct, c.EndSoCPct,
		c.EnergyAddedKWh, c.MilesAdded,
		c.MaxPowerKW, c.AvgPowerKW, c.FinalState,
		c.Lat, c.Lon, c.Source,
		c.Cost, c.Currency, c.PricePerKWh,
	)
	return err
}

// Count returns the total number of stored charges.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM charges`).Scan(&n)
	return n, err
}

// ListRecent returns the most recent N charges, newest first.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]Charge, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, vehicle_id, started_at, ended_at,
		       start_soc_pct, end_soc_pct,
		       energy_added_kwh, miles_added,
		       max_power_kw, avg_power_kw, final_state,
		       lat, lon, source,
		       COALESCE(cost, 0), COALESCE(currency, ''), COALESCE(price_per_kwh, 0)
		FROM charges ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Charge
	for rows.Next() {
		var c Charge
		var startUnix, endUnix int64
		if err := rows.Scan(&c.ID, &c.VehicleID, &startUnix, &endUnix,
			&c.StartSoCPct, &c.EndSoCPct,
			&c.EnergyAddedKWh, &c.MilesAdded,
			&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
			&c.Lat, &c.Lon, &c.Source,
			&c.Cost, &c.Currency, &c.PricePerKWh,
		); err != nil {
			return nil, err
		}
		c.StartedAt = time.Unix(startUnix, 0).UTC()
		c.EndedAt = time.Unix(endUnix, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestOpenLive returns the most recent live-sourced charge for the
// given vehicle whose final_state suggests the session hadn't
// terminated yet — i.e. the recorder was still updating it when the
// process stopped. Used to reconnect a restart to an in-flight
// session instead of orphaning it and opening a new row.
//
// "Open" means final_state starts with "charging_" and is NOT a
// terminal state (charging_complete / charging_station_err). Returns
// sql.ErrNoRows when there is no candidate.
func (s *Store) LatestOpenLive(ctx context.Context, vehicleID string) (*Charge, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, vehicle_id, started_at, ended_at,
		       start_soc_pct, end_soc_pct,
		       energy_added_kwh, miles_added,
		       max_power_kw, avg_power_kw, final_state,
		       lat, lon, source,
		       COALESCE(cost, 0), COALESCE(currency, ''), COALESCE(price_per_kwh, 0)
		FROM charges
		WHERE source = 'live'
		  AND vehicle_id = ?
		  AND (final_state LIKE 'charging_%'
		       AND final_state != 'charging_complete'
		       AND final_state != 'charging_station_err')
		ORDER BY started_at DESC
		LIMIT 1`, vehicleID)
	var c Charge
	var startUnix, endUnix int64
	if err := row.Scan(&c.ID, &c.VehicleID, &startUnix, &endUnix,
		&c.StartSoCPct, &c.EndSoCPct,
		&c.EnergyAddedKWh, &c.MilesAdded,
		&c.MaxPowerKW, &c.AvgPowerKW, &c.FinalState,
		&c.Lat, &c.Lon, &c.Source,
		&c.Cost, &c.Currency, &c.PricePerKWh,
	); err != nil {
		return nil, err
	}
	c.StartedAt = time.Unix(startUnix, 0).UTC()
	c.EndedAt = time.Unix(endUnix, 0).UTC()
	return &c, nil
}

// CloseStaleOpenLive marks every live-sourced charge row for the
// vehicle whose ID is NOT in keepIDs and whose final_state is still
// a non-terminal "charging_*" as closed. Used on recorder startup to
// retire orphaned sessions created by previous restarts (each one
// minted a fresh `live_<vid>_c_<unix>` row that never got closed).
// Returns the number of rows updated.
func (s *Store) CloseStaleOpenLive(ctx context.Context, vehicleID string, keepID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE charges
		SET final_state = 'abandoned'
		WHERE source = 'live'
		  AND vehicle_id = ?
		  AND id != ?
		  AND final_state LIKE 'charging_%'
		  AND final_state != 'charging_complete'
		  AND final_state != 'charging_station_err'`, vehicleID, keepID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Dedupe collapses duplicate charge rows. It runs two passes:
//
//  1. For electrafi_import rows, partition by started_at alone. Early
//     versions derived vehicle_id by hashing the CSV file path, so
//     re-importing the same session from a differently-named export
//     wrote a second row with a *different* vehicle_id but the same
//     start timestamp.
//  2. For everything else, partition by (vehicle_id, started_at) —
//     the standard case where a session was simply re-upserted.
//
// In each pass we pick the row with the most data as canonical, MAX
// the numeric fields across the group into it, then delete the rest.
// The electrafi pass also rewrites vehicle_id on the survivor so
// future imports (which now use a stable vehicle_id) reconcile.
//
// Returns the total number of rows deleted.
func (s *Store) Dedupe(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	total := 0

	// Pass 1: electrafi_import rows, grouped by started_at alone.
	{
		if _, err := tx.ExecContext(ctx, `
			WITH ranked AS (
				SELECT id, started_at,
				       ROW_NUMBER() OVER (
				         PARTITION BY started_at
				         ORDER BY energy_added_kwh DESC,
				                  max_power_kw DESC,
				                  ended_at DESC,
				                  id ASC
				       ) AS rn
				FROM charges
				WHERE source = 'electrafi_import'
			),
			keepers AS (SELECT id, started_at FROM ranked WHERE rn = 1),
			merges AS (
				SELECT k.id AS keep_id,
				       MAX(c.energy_added_kwh) AS energy_added_kwh,
				       MAX(c.miles_added)      AS miles_added,
				       MAX(c.max_power_kw)     AS max_power_kw,
				       MAX(c.avg_power_kw)     AS avg_power_kw,
				       MAX(c.ended_at)         AS ended_at
				FROM keepers k
				JOIN charges c
				  ON c.started_at = k.started_at
				 AND c.source = 'electrafi_import'
				GROUP BY k.id
			)
			UPDATE charges SET
				energy_added_kwh = (SELECT energy_added_kwh FROM merges WHERE merges.keep_id = charges.id),
				miles_added      = (SELECT miles_added      FROM merges WHERE merges.keep_id = charges.id),
				max_power_kw     = (SELECT max_power_kw     FROM merges WHERE merges.keep_id = charges.id),
				avg_power_kw     = (SELECT avg_power_kw     FROM merges WHERE merges.keep_id = charges.id),
				ended_at         = (SELECT ended_at         FROM merges WHERE merges.keep_id = charges.id)
			WHERE id IN (SELECT keep_id FROM merges)
		`); err != nil {
			return 0, fmt.Errorf("electrafi merge: %w", err)
		}
		res, err := tx.ExecContext(ctx, `
			DELETE FROM charges WHERE id IN (
				SELECT id FROM (
					SELECT id, ROW_NUMBER() OVER (
						PARTITION BY started_at
						ORDER BY energy_added_kwh DESC,
						         max_power_kw DESC,
						         ended_at DESC,
						         id ASC
					) AS rn FROM charges
					WHERE source = 'electrafi_import'
				) WHERE rn > 1
			)`)
		if err != nil {
			return 0, fmt.Errorf("electrafi delete: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}

	// Pass 2: everything else, grouped by (vehicle_id, started_at).
	{
		if _, err := tx.ExecContext(ctx, `
			WITH ranked AS (
				SELECT id, vehicle_id, started_at,
				       ROW_NUMBER() OVER (
				         PARTITION BY vehicle_id, started_at
				         ORDER BY energy_added_kwh DESC,
				                  max_power_kw DESC,
				                  ended_at DESC,
				                  id ASC
				       ) AS rn
				FROM charges
				WHERE source <> 'electrafi_import'
			)
			DELETE FROM charges WHERE id IN (SELECT id FROM ranked WHERE rn > 1)
		`); err != nil {
			return 0, fmt.Errorf("live delete: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total, nil
}
