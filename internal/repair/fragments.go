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
	// PackKWh is the vehicle's pack capacity (nil = unknown).
	PackKWh *float64
	// DriveBefore is true when a drive of the same vehicle falls
	// between the previous live charge's end and this piece's start.
	DriveBefore bool
	// HasRestamp is true when a charge_restamps row (a user's place
	// correction) points at this piece.
	HasRestamp bool
}

// defaultPackKWh is used for the own-SoC-gain estimate when a vehicle
// has no recorded pack capacity.
const defaultPackKWh = 135.0

// Skipped is a candidate group the repair refuses to touch.
type Skipped struct {
	Group  Group
	Reason string
}

// Group is a run of two or more pieces of one session, in time order.
// The keeper is the LAST piece that has an energy value; every other
// piece (including null-energy ones) is removed.
type Group struct{ Pieces []Piece }

func (g Group) keeperIndex() int {
	for i := len(g.Pieces) - 1; i >= 0; i-- {
		if g.Pieces[i].EnergyKWh != nil {
			return i
		}
	}
	return -1
}

// Keeper is the row that survives (the zero Piece if no piece has
// energy; FindGroups never returns such a group).
func (g Group) Keeper() Piece {
	if i := g.keeperIndex(); i >= 0 {
		return g.Pieces[i]
	}
	return Piece{}
}

// Removed are the rows deleted.
func (g Group) Removed() []Piece {
	k := g.keeperIndex()
	var out []Piece
	for i, p := range g.Pieces {
		if i != k {
			out = append(out, p)
		}
	}
	return out
}

// Merged is what the keeper becomes.
type Merged struct {
	StartedAt     time.Time
	EndedAt       time.Time
	FinalState    string
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

// Plan computes the merged row. The keeper keeps its own energy (the
// session's running total) and its own cost (priced on that total); it
// takes the first piece's start and start SoC, the peak power, the
// summed active seconds, and the last piece's end and final state.
func (g Group) Plan() Merged {
	first, keeper, last := g.Pieces[0], g.Keeper(), g.Pieces[len(g.Pieces)-1]
	m := Merged{StartedAt: first.StartedAt, StartSoC: first.StartSoC, EndedAt: last.EndedAt, FinalState: last.FinalState}
	if keeper.EndedAt.After(m.EndedAt) {
		m.EndedAt = keeper.EndedAt
	}
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

// FindGroups returns the fragment runs in ps and the runs it refuses.
//
// Pieces chain when they are live charges of one user and vehicle, the
// next starts within maxGap of the previous end (stale-gear flaps leave
// ~3 s gaps and ~178 s gaps, one resubscribe TTL), the previous was
// closed 'abandoned', no drive happened in between, and the next piece
// is a RUNNING TOTAL rather than an independent session:
//
//   - its energy is not below the previous energy, and
//   - it claims more than its own SoC gain can explain
//     (energy > 1.3 x own gain + 1 kWh), or it repeats the previous
//     energy while its own SoC gain is ~0 (a stale duplicate).
//
// A piece whose SoC gain explains its energy is its own session (a
// real unplug and re-plug) and ends the run. Missing SoC data means
// "cannot verify": the run ends. Null-energy pieces ride along (they
// are removed, never kept). A group needs two or more pieces and at
// least one with energy. A group whose removed pieces carry a user
// price or a restamp is returned as skipped, not merged.
func FindGroups(ps []Piece, maxGap time.Duration) ([]Group, []Skipped) {
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
	var skipped []Skipped
	var cur []Piece
	lastEnergy := func() *float64 {
		for i := len(cur) - 1; i >= 0; i-- {
			if cur[i].EnergyKWh != nil {
				return cur[i].EnergyKWh
			}
		}
		return nil
	}
	flush := func() {
		if len(cur) >= 2 {
			g := Group{Pieces: cur}
			if g.keeperIndex() >= 0 {
				if reason := refuse(g); reason != "" {
					skipped = append(skipped, Skipped{Group: g, Reason: reason})
				} else {
					out = append(out, g)
				}
			}
		}
		cur = nil
	}
	continues := func(prev, next Piece) bool {
		if prev.UserID != next.UserID || prev.VehicleID != next.VehicleID {
			return false
		}
		if prev.Source != "live" || next.Source != "live" || prev.FinalState != "abandoned" || next.DriveBefore {
			return false
		}
		gap := next.StartedAt.Sub(prev.EndedAt)
		if gap < -maxGap || gap > maxGap {
			return false
		}
		if next.EnergyKWh == nil {
			return true // rides along; removed later
		}
		return runningTotal(lastEnergy(), next)
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
	return out, skipped
}

// runningTotal reports whether next's energy is a running session
// total (see FindGroups) given the last known energy in the run.
func runningTotal(lastE *float64, next Piece) bool {
	if next.EnergyKWh == nil || next.StartSoC == nil || next.EndSoC == nil {
		return false
	}
	e := *next.EnergyKWh
	if lastE != nil && e < *lastE {
		return false
	}
	pack := defaultPackKWh
	if next.PackKWh != nil && *next.PackKWh > 0 {
		pack = *next.PackKWh
	}
	gainPct := *next.EndSoC - *next.StartSoC
	if gainPct < 0 {
		gainPct = 0
	}
	ownKWh := gainPct / 100 * pack
	if e > 1.3*ownKWh+1 {
		return true
	}
	// A stale duplicate: same energy as before with no SoC gain.
	return lastE != nil && e-*lastE <= 0.05 && gainPct < 0.5
}

// refuse says why a group must not be merged ("" = fine): deleting a
// piece would drop a price the user entered or a place correction.
func refuse(g Group) string {
	for _, p := range g.Removed() {
		if strings.EqualFold(p.CostSource, "user") {
			return "a removed piece has a user-entered price (" + p.ExternalID + ")"
		}
		if p.HasRestamp {
			return "a removed piece has a place correction (" + p.ExternalID + ")"
		}
	}
	return ""
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
	       v.pack_kwh,
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
SELECT c.*,
       (prev_end IS NOT NULL AND EXISTS (
            SELECT 1 FROM drives d
             WHERE d.user_id = c.user_id AND d.vehicle_id = c.vehicle_id
               AND d.ended_at > c.prev_end AND d.started_at < c.started_at)) AS drive_before,
       EXISTS (SELECT 1 FROM charge_restamps r WHERE r.user_id = c.user_id AND r.charge_id = c.id) AS has_restamp
  FROM c
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
		var startSoC, endSoC, energy, maxPow, active, cost, pack sql.NullFloat64
		var prevEnd, nextStart sql.NullTime
		if err := rows.Scan(&p.ID, &p.ExternalID, &p.UserID, &p.Username, &p.VehicleID, &p.RivianID,
			&p.StartedAt, &p.EndedAt, &startSoC, &endSoC, &energy, &maxPow, &active, &cost, &p.Currency,
			&p.FinalState, &p.CostSource, &p.Source, &pack, &prevEnd, &nextStart,
			&p.DriveBefore, &p.HasRestamp); err != nil {
			return nil, err
		}
		p.StartSoC, p.EndSoC, p.EnergyKWh = nf(startSoC), nf(endSoC), nf(energy)
		p.MaxPowerKW, p.ActiveSeconds, p.Cost = nf(maxPow), nf(active), nf(cost)
		p.PackKWh = nf(pack)
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
// session's start, start SoC, end, final state, peak power and active
// seconds, and the other pieces are deleted. Every statement is scoped
// by user_id and guarded by the rows still being what Load saw.
func Apply(ctx context.Context, d *sql.DB, g Group) error {
	keeper := g.Keeper()
	if keeper.ID == uuid.Nil {
		return fmt.Errorf("group has no piece with energy")
	}
	m := g.Plan()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE charges SET started_at = $3::timestamptz, ended_at = $8::timestamptz,
		       final_state = COALESCE(NULLIF($9, ''), final_state),
		       start_soc_pct = COALESCE($4, start_soc_pct),
		       max_power_kw = COALESCE($5, max_power_kw),
		       active_seconds = COALESCE($6, active_seconds),
		       energy_added_kwh = COALESCE(energy_added_kwh, $7),
		       updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND source = 'live'`,
		keeper.ID, keeper.UserID, m.StartedAt.UTC(), nullF(m.StartSoC), nullF(m.MaxPowerKW), nullF(m.ActiveSeconds),
		nullF(m.EnergyKWh), m.EndedAt.UTC(), m.FinalState)
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
