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
    source           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS charges_started_at ON charges(started_at);
CREATE INDEX IF NOT EXISTS charges_vehicle_id ON charges(vehicle_id);
`

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
			lat, lon, source
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
			source           = excluded.source`,
		c.ID, c.VehicleID, c.StartedAt.Unix(), c.EndedAt.Unix(),
		c.StartSoCPct, c.EndSoCPct,
		c.EnergyAddedKWh, c.MilesAdded,
		c.MaxPowerKW, c.AvgPowerKW, c.FinalState,
		c.Lat, c.Lon, c.Source,
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
		       lat, lon, source
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
		); err != nil {
			return nil, err
		}
		c.StartedAt = time.Unix(startUnix, 0).UTC()
		c.EndedAt = time.Unix(endUnix, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
