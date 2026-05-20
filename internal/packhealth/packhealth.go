// Package packhealth derives an "effective pack capacity" per
// qualifying charge session. The result is a per-vehicle time
// series the UI plots to surface battery degradation — Rivian
// itself doesn't expose a SoH field (verified against the 3.12
// Android app), so we have to compute it from inputs we already
// record on the `charges` table.
//
// # The fit
//
//	pack_kwh_effective = energy_added_kwh / (soc_delta / 100)
//
// where soc_delta = end_soc_pct - start_soc_pct.
//
// # Qualification
//
// Small SoC deltas amplify any measurement noise on energy_added_kwh
// (a 5%-window session gives a 20x noise multiplier on the divisor).
// We discard sessions with delta < MinSoCDeltaPct. Energy must be
// positive and below SanityMaxEnergyKWh — values above that almost
// always indicate a multi-day session that wrapped around midnight
// in the importer.
//
// # What's NOT yet captured
//
// The recorder doesn't currently persist whether chargerDerateStatus
// was "active" during a session; until it does, we always store
// DerateActive=false. The trend-line UI should still treat the column
// as authoritative once the recorder hook starts populating it.
package packhealth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MinSoCDeltaPct is the smallest SoC delta we trust to produce a
// meaningful pack-capacity estimate. Below this the energy-vs-delta
// ratio is dominated by measurement noise (the SoC field is
// quantised to whole percent on Rivian's side; a 5% session has
// ~10% noise on the divisor). 30% is a good compromise — it admits
// most real DC fast stops and rejects topping-off home sessions.
const MinSoCDeltaPct = 30.0

// SanityMaxEnergyKWh rejects obvious bad data (multi-day sessions
// that the importer collapsed into a single row, etc.). Standard+
// R1T usable is ~131 kWh; 200 is a comfortable upper bound.
const SanityMaxEnergyKWh = 200.0

// Sample is a single derived data point for a charge session.
type Sample struct {
	VehicleID          uuid.UUID `json:"vehicle_id"`
	ChargeID           uuid.UUID `json:"charge_id"`
	At                 time.Time `json:"at"`
	PackKWhEffective   float64   `json:"pack_kwh_effective"`
	SoCDeltaPct        float64   `json:"soc_delta_pct"`
	EnergyDeliveredKWh float64   `json:"energy_delivered_kwh"`
	DerateActive       bool      `json:"derate_active"`
}

// ChargeInput is the subset of a charge row Derive needs. Keeping
// it a small struct (instead of importing db.Charge) avoids an
// import cycle and makes the math testable without a DB.
type ChargeInput struct {
	VehicleID       uuid.UUID
	ChargeID        uuid.UUID
	EndedAt         time.Time
	StartSoCPct     float64
	EndSoCPct       float64
	EnergyAddedKWh  float64
	DerateObserved  bool
}

// Derive computes a Sample for a charge. Returns ok=false when the
// input doesn't qualify (small SoC delta, missing energy, sanity
// reject). Callers should silently skip — non-qualifying sessions
// aren't an error condition, just data that can't be fit cleanly.
func Derive(in ChargeInput) (Sample, bool) {
	delta := in.EndSoCPct - in.StartSoCPct
	if delta < MinSoCDeltaPct {
		return Sample{}, false
	}
	if in.EnergyAddedKWh <= 0 || in.EnergyAddedKWh > SanityMaxEnergyKWh {
		return Sample{}, false
	}
	pack := in.EnergyAddedKWh / (delta / 100.0)
	// Final sanity: a Tri-Motor Max with a 141 kWh pack should
	// never come back as 250 kWh effective. Cap at 1.5× the
	// largest sane nameplate; anything beyond is corrupt input.
	if pack > 250 {
		return Sample{}, false
	}
	return Sample{
		VehicleID:          in.VehicleID,
		ChargeID:           in.ChargeID,
		At:                 in.EndedAt,
		PackKWhEffective:   pack,
		SoCDeltaPct:        delta,
		EnergyDeliveredKWh: in.EnergyAddedKWh,
		DerateActive:       in.DerateObserved,
	}, true
}

// Store wraps the vehicle_pack_health_samples table.
type Store struct {
	db *sql.DB
}

// NewStore returns a store over the shared pool. Multi-tenant
// scoping is enforced by the caller passing vehicleID — Postgres
// RLS on the charges table guards against cross-tenant reads at
// the source-of-truth level.
func NewStore(d *sql.DB) *Store {
	return &Store{db: d}
}

// Upsert inserts a sample, replacing on conflict so re-running
// Derive on a charge whose energy was later corrected refreshes the
// row instead of duplicating it.
func (s *Store) Upsert(ctx context.Context, sample Sample) error {
	if s == nil || s.db == nil {
		return errors.New("packhealth: nil store")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO vehicle_pack_health_samples
			(vehicle_id, charge_id, at, pack_kwh_effective,
			 soc_delta_pct, energy_delivered_kwh, derate_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (vehicle_id, charge_id) DO UPDATE SET
			at                   = EXCLUDED.at,
			pack_kwh_effective   = EXCLUDED.pack_kwh_effective,
			soc_delta_pct        = EXCLUDED.soc_delta_pct,
			energy_delivered_kwh = EXCLUDED.energy_delivered_kwh,
			derate_active        = EXCLUDED.derate_active
	`,
		sample.VehicleID, sample.ChargeID, sample.At,
		sample.PackKWhEffective, sample.SoCDeltaPct,
		sample.EnergyDeliveredKWh, sample.DerateActive,
	)
	if err != nil {
		return fmt.Errorf("upsert pack health sample: %w", err)
	}
	return nil
}

// ListByVehicle returns samples for a vehicle, oldest first so the
// UI can plot a left-to-right trend directly. limit caps the row
// count (0 = unlimited; callers usually pass 365 for a year view).
func (s *Store) ListByVehicle(ctx context.Context, vehicleID uuid.UUID, limit int) ([]Sample, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	q := `
		SELECT vehicle_id, charge_id, at,
		       pack_kwh_effective, soc_delta_pct, energy_delivered_kwh,
		       derate_active
		FROM vehicle_pack_health_samples
		WHERE vehicle_id = $1
		ORDER BY at ASC`
	args := []any{vehicleID}
	if limit > 0 {
		q += " LIMIT $2"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("select pack health samples: %w", err)
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var x Sample
		if err := rows.Scan(
			&x.VehicleID, &x.ChargeID, &x.At,
			&x.PackKWhEffective, &x.SoCDeltaPct, &x.EnergyDeliveredKWh,
			&x.DerateActive,
		); err != nil {
			return nil, fmt.Errorf("scan pack health sample: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// BackfillAll iterates every vehicle and runs BackfillVehicle.
// Idempotent (Upsert replaces on conflict). Designed to be called
// from a startup hydrate sweep AND a recurring ticker so newly
// recorded charges become samples without a per-charge recorder
// hook. Returns the total count of inserted/updated rows across
// all vehicles.
func (s *Store) BackfillAll(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM vehicles`)
	if err != nil {
		return 0, fmt.Errorf("select vehicles: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var v uuid.UUID
		if err := rows.Scan(&v); err != nil {
			return 0, fmt.Errorf("scan vehicle id: %w", err)
		}
		ids = append(ids, v)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var total int
	for _, v := range ids {
		n, err := s.BackfillVehicle(ctx, v)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// BackfillVehicle re-derives samples for every existing charge on a
// vehicle. Idempotent (Upsert replaces on conflict) so safe to
// re-run any time. Skips charges where Derive returns ok=false.
//
// Reads start_soc_pct, end_soc_pct, energy_added_kwh, ended_at
// directly from the charges table. Bypasses RLS — pass a trusted
// vehicleID that the caller has already authorized.
func (s *Store) BackfillVehicle(ctx context.Context, vehicleID uuid.UUID) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ended_at,
		       COALESCE(start_soc_pct, 0),
		       COALESCE(end_soc_pct, 0),
		       COALESCE(energy_added_kwh, 0)
		FROM charges
		WHERE vehicle_id = $1
		  AND ended_at IS NOT NULL
		  AND start_soc_pct IS NOT NULL
		  AND end_soc_pct IS NOT NULL
		  AND energy_added_kwh IS NOT NULL
	`, vehicleID)
	if err != nil {
		return 0, fmt.Errorf("select charges for backfill: %w", err)
	}
	defer rows.Close()
	var inserted int
	for rows.Next() {
		var (
			chargeID uuid.UUID
			endedAt  time.Time
			start    float64
			end      float64
			energy   float64
		)
		if err := rows.Scan(&chargeID, &endedAt, &start, &end, &energy); err != nil {
			return inserted, fmt.Errorf("scan charge: %w", err)
		}
		sample, ok := Derive(ChargeInput{
			VehicleID:      vehicleID,
			ChargeID:       chargeID,
			EndedAt:        endedAt,
			StartSoCPct:    start,
			EndSoCPct:      end,
			EnergyAddedKWh: energy,
		})
		if !ok {
			continue
		}
		if err := s.Upsert(ctx, sample); err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, rows.Err()
}
