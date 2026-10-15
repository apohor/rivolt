// Package repair holds one-off data repairs run from the rivolt CLI.
//
// Fragments (repair-charge-fragments): a single physical charging
// session that the recorder stored as several back-to-back rows, each
// carrying the session's RUNNING energy total, so the ledger counts
// the energy once per piece (2026-10-10: 11 pieces, 496.6 kWh counted
// for ~82 kWh). Cause: a replayed stale Parallax gear frame made the
// recorder close and reopen the charge on every resubscribe. This
// package finds those runs and merges each into one row.
//
// Everything here is dry-run first and reversible: the caller writes a
// CSV backup of every touched row before Apply.
package repair

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Piece is one charges row considered for merging.
type Piece struct {
	ID            uuid.UUID
	ExternalID    string
	UserID        uuid.UUID
	Username      string
	VehicleID     uuid.UUID
	RivianID      string
	StartedAt     time.Time
	EndedAt       time.Time
	StartSoC      *float64
	EndSoC        *float64
	EnergyKWh     *float64
	MaxPowerKW    *float64
	ActiveSeconds *float64
	Cost          *float64
	Currency      string
	FinalState    string
	CostSource    string
	Source        string
}

// Group is a run of two or more pieces of one session, in time order.
// The last piece is the keeper.
type Group struct{ Pieces []Piece }

// Keeper is the row that survives.
func (g Group) Keeper() Piece { return g.Pieces[len(g.Pieces)-1] }

// Removed are the rows deleted.
func (g Group) Removed() []Piece { return g.Pieces[:len(g.Pieces)-1] }

// Merged is what the keeper becomes.
type Merged struct {
	StartedAt     time.Time
	StartSoC      *float64
	MaxPowerKW    *float64
	ActiveSeconds *float64
	EnergyKWh     *float64
	Cost          *float64
	// CountedKWh / CountedCost are what the pieces contribute today.
	CountedKWh  float64
	CountedCost float64
}

func fsum(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Plan computes the merged row. Energy is the keeper's running total
// (or the largest known piece total when the keeper has none); cost is
// the keeper's (it was priced on the final running energy).
func (g Group) Plan() Merged {
	first, keeper := g.Pieces[0], g.Keeper()
	m := Merged{StartedAt: first.StartedAt, StartSoC: first.StartSoC}
	var maxPow, active float64
	var haveMax, haveActive bool
	var maxEnergy float64
	haveEnergy := false
	for _, p := range g.Pieces {
		m.CountedKWh += fsum(p.EnergyKWh)
		m.CountedCost += fsum(p.Cost)
		if p.MaxPowerKW != nil && (!haveMax || *p.MaxPowerKW > maxPow) {
			maxPow, haveMax = *p.MaxPowerKW, true
		}
		if p.ActiveSeconds != nil {
			active += *p.ActiveSeconds
			haveActive = true
		}
		if p.EnergyKWh != nil && (!haveEnergy || *p.EnergyKWh > maxEnergy) {
			maxEnergy, haveEnergy = *p.EnergyKWh, true
		}
	}
	if haveMax {
		m.MaxPowerKW = &maxPow
	}
	if haveActive {
		m.ActiveSeconds = &active
	}
	switch {
	case keeper.EnergyKWh != nil:
		e := *keeper.EnergyKWh
		m.EnergyKWh = &e
	case haveEnergy:
		m.EnergyKWh = &maxEnergy
	}
	if keeper.Cost != nil {
		c := *keeper.Cost
		m.Cost = &c
	}
	return m
}

// FindGroups returns the fragment runs in ps. Pieces must be live
// sessions of the same user and vehicle, each next piece starting
// within maxGap of the previous one's end, every piece but the last
// closed 'abandoned', and the energy never going down (it is a running
// total). A lone row is never a group.
func FindGroups(ps []Piece, maxGap time.Duration) []Group {
	sorted := append([]Piece(nil), ps...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.UserID != b.UserID {
			return a.UserID.String() < b.UserID.String()
		}
		if a.VehicleID != b.VehicleID {
			return a.VehicleID.String() < b.VehicleID.String()
		}
		return a.StartedAt.Before(b.StartedAt)
	})
	var out []Group
	var cur []Piece
	flush := func() {
		if len(cur) >= 2 {
			out = append(out, Group{Pieces: cur})
		}
		cur = nil
	}
	continues := func(prev, next Piece) bool {
		if prev.UserID != next.UserID || prev.VehicleID != next.VehicleID {
			return false
		}
		if prev.Source != "live" || next.Source != "live" || prev.FinalState != "abandoned" {
			return false
		}
		gap := next.StartedAt.Sub(prev.EndedAt)
		if gap < -maxGap || gap > maxGap {
			return false
		}
		if prev.EnergyKWh != nil && next.EnergyKWh != nil && *next.EnergyKWh < *prev.EnergyKWh {
			return false
		}
		return true
	}
	for _, p := range sorted {
		if p.Source != "live" {
			flush()
			continue
		}
		if len(cur) > 0 && continues(cur[len(cur)-1], p) {
			cur = append(cur, p)
			continue
		}
		flush()
		cur = []Piece{p}
	}
	flush()
	return out
}

// Load reads the candidate pieces: live charges within maxGap of
// another live charge of the same vehicle. user ("" = all) filters by
// username; since/until (zero = open) bound started_at.
func Load(ctx context.Context, d *sql.DB, user string, since, until time.Time, maxGap time.Duration) ([]Piece, error) {
	secs := maxGap.Seconds()
	rows, err := d.QueryContext(ctx, `
WITH c AS (
	SELECT c.id, c.external_id, c.user_id, u.username, c.vehicle_id, v.rivian_vehicle_id,
	       c.started_at, c.ended_at, c.start_soc_pct, c.end_soc_pct, c.energy_added_kwh,
	       c.max_power_kw, c.active_seconds::float8, c.cost::float8, COALESCE(c.currency,''),
	       COALESCE(c.final_state,''), COALESCE(c.cost_source,''), c.source,
	       LAG(c.ended_at)    OVER w AS prev_end,
	       LEAD(c.started_at) OVER w AS next_start
	  FROM charges c
	  JOIN users u    ON u.id = c.user_id
	  JOIN vehicles v ON v.id = c.vehicle_id
	 WHERE c.source = 'live'
	   AND ($1 = '' OR lower(u.username) = lower($1))
	   AND ($2::timestamptz IS NULL OR c.started_at >= $2::timestamptz)
	   AND ($3::timestamptz IS NULL OR c.started_at <  $3::timestamptz)
	WINDOW w AS (PARTITION BY c.vehicle_id ORDER BY c.started_at)
)
SELECT * FROM c
 WHERE abs(EXTRACT(EPOCH FROM (started_at - prev_end))) <= $4
    OR abs(EXTRACT(EPOCH FROM (next_start - ended_at))) <= $4
 ORDER BY user_id, vehicle_id, started_at`,
		user, nullTime(since), nullTime(until), secs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Piece
	for rows.Next() {
		var p Piece
		var startSoC, endSoC, energy, maxPow, active, cost sql.NullFloat64
		var prevEnd, nextStart sql.NullTime
		if err := rows.Scan(&p.ID, &p.ExternalID, &p.UserID, &p.Username, &p.VehicleID, &p.RivianID,
			&p.StartedAt, &p.EndedAt, &startSoC, &endSoC, &energy, &maxPow, &active, &cost, &p.Currency,
			&p.FinalState, &p.CostSource, &p.Source, &prevEnd, &nextStart); err != nil {
			return nil, err
		}
		p.StartSoC, p.EndSoC, p.EnergyKWh = nf(startSoC), nf(endSoC), nf(energy)
		p.MaxPowerKW, p.ActiveSeconds, p.Cost = nf(maxPow), nf(active), nf(cost)
		p.StartedAt, p.EndedAt = p.StartedAt.UTC(), p.EndedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func nf(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// Apply merges one group in a single transaction: the keeper takes the
// session's start, start SoC, peak power and active seconds, and the
// other pieces are deleted. Every statement is scoped by user_id and
// guarded by the keeper/pieces still being what Load saw.
func Apply(ctx context.Context, d *sql.DB, g Group) error {
	keeper := g.Keeper()
	m := g.Plan()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE charges SET started_at = $3::timestamptz,
		       start_soc_pct = COALESCE($4, start_soc_pct),
		       max_power_kw = COALESCE($5, max_power_kw),
		       active_seconds = COALESCE($6, active_seconds),
		       energy_added_kwh = COALESCE(energy_added_kwh, $7),
		       updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND source = 'live'`,
		keeper.ID, keeper.UserID, m.StartedAt.UTC(), nullF(m.StartSoC), nullF(m.MaxPowerKW), nullF(m.ActiveSeconds), nullF(m.EnergyKWh))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("keeper %s not found (changed since it was read)", keeper.ExternalID)
	}
	ids := make([]string, 0, len(g.Removed()))
	for _, p := range g.Removed() {
		ids = append(ids, p.ID.String())
	}
	res, err = tx.ExecContext(ctx, `
		DELETE FROM charges
		 WHERE user_id = $1 AND source = 'live' AND id = ANY($2::uuid[])`,
		keeper.UserID, "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); int(n) != len(ids) {
		return fmt.Errorf("deleted %d of %d pieces; rolling back", n, len(ids))
	}
	return tx.Commit()
}

func nullF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
