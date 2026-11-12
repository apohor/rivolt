package receipts

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
)

// Receipt energy. The recorder takes a DC session's energy from the last frame of its live feed, and
// that feed can stop short of the session's end (a few kWh at most, the tail), so a matched Rivian
// receipt, which is Rivian's own total, can be the better figure. The sync takes it for a closed charge
// when it passes a one-sided physical check, keeps what we recorded in the charge's metadata, and marks
// the source. It never moves ended_at and never touches a user's own price or Rivian's billed amount.

// Energy modes (RIVOLT_RECEIPT_ENERGY): Observe measures and counts only, Apply also writes.
const (
	EnergyOff     = "off"
	EnergyObserve = "observe"
	EnergyApply   = "apply"
)

// Outcomes of looking at one matched receipt, for the metrics.
const (
	OutcomeApplied    = "applied"
	OutcomeWouldApply = "would_apply"
	OutcomeHome       = "home"
	OutcomeNoDetail   = "no_detail"
	OutcomeNoEnergy   = "no_energy"
	OutcomeOpen       = "open"
	OutcomeBelow      = "below_recorded"
	OutcomeAbove      = "above_tail_limit"
)

// SourceReceipt is the metadata energy_source of a charge whose energy came from its receipt.
const SourceReceipt = "rivian_receipt"

const (
	// energyWindow is how far back a matched receipt is looked at on a sync: newly matched ones and
	// their refreshes. Older rows are the backfill's, a separate reviewed run.
	energyWindow = 72 * time.Hour
	// A receipt may be this much below what we recorded (rounding, meter basis), as a fraction.
	energyLowTol = 0.01
	// The tail allowance: charger power times the gap between our last frame and the receipt's end,
	// times energyTailSlack, plus energyTailFixed kWh.
	energyTailSlack = 1.2
	energyTailFixed = 0.2
)

// EnergyCand is one matched receipt next to its charge, as the check needs them.
type EnergyCand struct {
	ChargeID   uuid.UUID
	ReceiptID  uuid.UUID
	TxnID      string
	FinalState string
	ChargeEnd  time.Time
	MaxPowerKW float64
	// Energy is the charge's energy now; Recorded is what the recorder saved first (the metadata
	// original once a receipt value is in, else Energy); AvgRecorded and CostRecorded the same for
	// the average power and the cost.
	Energy, Recorded, AvgPower float64
	AvgRecorded                *float64
	Cost                       *float64
	CostRecorded               *float64
	CostSource                 string
	PricePerKWh                float64
	FromReceipt                bool

	ReceiptStart, ReceiptEnd time.Time
	ReceiptEnergy            *float64
	IsHome                   *bool
	DetailFetched            bool
}

// EnergyUpdate is what to write for a candidate that passed.
type EnergyUpdate struct {
	Energy      float64
	AvgPower    float64
	Cost        *float64 // nil: leave the cost alone
	Meta        map[string]any
	ExpectedNow float64
}

// EvaluateEnergy checks one candidate. outcome is OutcomeWouldApply when the receipt's energy should
// be taken (nil update means nothing to write: it already is), deltaPct the receipt's difference from
// what we recorded as a percentage of it (0 when not computed).
func EvaluateEnergy(c EnergyCand) (outcome string, deltaPct float64, up *EnergyUpdate) {
	switch {
	case c.IsHome != nil && *c.IsHome:
		return OutcomeHome, 0, nil
	case !c.DetailFetched || c.IsHome == nil:
		return OutcomeNoDetail, 0, nil
	case c.ReceiptEnergy == nil || *c.ReceiptEnergy <= 0 || c.Recorded <= 0:
		return OutcomeNoEnergy, 0, nil
	case c.FinalState == "charging_active":
		return OutcomeOpen, 0, nil
	}
	re := *c.ReceiptEnergy
	delta := re - c.Recorded
	deltaPct = delta / c.Recorded * 100
	if delta < -energyLowTol*c.Recorded {
		return OutcomeBelow, deltaPct, nil
	}
	tail := c.ReceiptEnd.Sub(c.ChargeEnd).Hours()
	if tail < 0 {
		tail = 0
	}
	if delta > c.MaxPowerKW*tail*energyTailSlack+energyTailFixed {
		return OutcomeAbove, deltaPct, nil
	}
	if c.FromReceipt && math.Abs(re-c.Energy) < 0.0005 {
		return OutcomeWouldApply, deltaPct, nil // already in
	}
	up = &EnergyUpdate{Energy: re, ExpectedNow: c.Energy, Meta: map[string]any{
		"energy_source":       SourceReceipt,
		"energy_recorded_kwh": round4(c.Recorded),
		"receipt_started_at":  c.ReceiptStart.UTC().Format(time.RFC3339),
		"receipt_ended_at":    c.ReceiptEnd.UTC().Format(time.RFC3339),
	}}
	up.AvgPower = c.AvgPower
	if d := c.ReceiptEnd.Sub(c.ReceiptStart).Hours(); d > 0 {
		up.AvgPower = round4(re / d)
		if c.AvgRecorded == nil {
			up.Meta["avg_power_recorded_kw"] = round4(c.AvgPower)
		}
	}
	// A price-based cost follows the energy; a price the user typed and Rivian's billed amount do not.
	if c.Cost != nil && *c.Cost > 0 && c.PricePerKWh > 0 && c.CostSource != "user" && c.CostSource != "rivian" {
		nc := round4(c.PricePerKWh * re)
		up.Cost = &nc
		up.Meta["cost_applied"] = nc
		if c.CostRecorded == nil {
			up.Meta["cost_recorded"] = round4(*c.Cost)
		}
	}
	return OutcomeWouldApply, deltaPct, up
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// EnergyCandidates lists the user's receipts matched since `since` with their charges.
func (s *Store) EnergyCandidates(ctx context.Context, uid uuid.UUID, since time.Time) ([]EnergyCand, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, r.id, r.transaction_id, COALESCE(c.final_state,''), c.ended_at,
		       COALESCE(c.max_power_kw,0)::float8, COALESCE(c.energy_added_kwh,0)::float8,
		       COALESCE((c.metadata->>'energy_recorded_kwh')::float8, c.energy_added_kwh, 0)::float8,
		       COALESCE(c.avg_power_kw,0)::float8, (c.metadata->>'avg_power_recorded_kw')::float8,
		       c.cost::float8, (c.metadata->>'cost_recorded')::float8, COALESCE(c.cost_source,''),
		       COALESCE(c.price_per_kwh,0)::float8, COALESCE(c.metadata->>'energy_source','') = $3,
		       r.started_at, r.ended_at, r.energy_kwh, r.is_home_charger, r.detail_fetched_at IS NOT NULL
		  FROM rivian_receipts r
		  JOIN charges c ON c.id = r.charge_id AND c.user_id = r.user_id AND c.source = 'live'
		 WHERE r.user_id = $1 AND r.matched_at >= $2
		 ORDER BY r.started_at`, uid, since, SourceReceipt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnergyCand
	for rows.Next() {
		var c EnergyCand
		var avgRec, cost, costRec, kwh sql.NullFloat64
		var home sql.NullBool
		if err := rows.Scan(&c.ChargeID, &c.ReceiptID, &c.TxnID, &c.FinalState, &c.ChargeEnd, &c.MaxPowerKW, &c.Energy, &c.Recorded,
			&c.AvgPower, &avgRec, &cost, &costRec, &c.CostSource, &c.PricePerKWh, &c.FromReceipt,
			&c.ReceiptStart, &c.ReceiptEnd, &kwh, &home, &c.DetailFetched); err != nil {
			return nil, err
		}
		c.AvgRecorded, c.Cost, c.CostRecorded, c.ReceiptEnergy = floatPtr(avgRec), floatPtr(cost), floatPtr(costRec), floatPtr(kwh)
		if home.Valid {
			b := home.Bool
			c.IsHome = &b
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ApplyEnergy writes one update to the charge, only while it is still closed, still linked to the
// receipt and still holding the energy the check saw. It reports whether a row changed.
func (s *Store) ApplyEnergy(ctx context.Context, uid uuid.UUID, c EnergyCand, up EnergyUpdate) (bool, error) {
	meta, err := json.Marshal(up.Meta)
	if err != nil {
		return false, err
	}
	n, err := fence.Exec(ctx, s.db, `
		UPDATE charges
		   SET energy_added_kwh = $4, avg_power_kw = $5, cost = COALESCE($6, cost),
		       metadata = metadata || $7::jsonb, updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND receipt_txn_id = $3 AND source = 'live'
		   AND COALESCE(final_state,'') <> 'charging_active'
		   AND COALESCE(energy_added_kwh,0)::float8 = $8 AND `+fence.OK,
		c.ChargeID, uid, c.TxnID, up.Energy, up.AvgPower, up.Cost, string(meta), up.ExpectedNow)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// restoreEnergySQL puts back what the recorder saved on charges whose energy came from a receipt, and
// clears the marker keys. pred selects the users (see purge).
const restoreEnergySQL = `
	UPDATE charges
	   SET energy_added_kwh = COALESCE((metadata->>'energy_recorded_kwh')::float8, energy_added_kwh),
	       avg_power_kw = COALESCE((metadata->>'avg_power_recorded_kw')::float8, avg_power_kw),
	       cost = CASE WHEN cost_source = 'user' OR cost_source = 'rivian' THEN cost
	                   WHEN round(cost::numeric, 4) = round((metadata->>'cost_applied')::numeric, 4)
	                   THEN COALESCE((metadata->>'cost_recorded')::float8, cost)
	                   WHEN COALESCE(price_per_kwh, 0) > 0
	                    AND round(cost::numeric, 4) = round((price_per_kwh * energy_added_kwh)::numeric, 4)
	                   THEN round((price_per_kwh * COALESCE((metadata->>'energy_recorded_kwh')::float8, energy_added_kwh))::numeric, 4)
	                   ELSE cost END,
	       metadata = metadata - 'energy_source' - 'energy_recorded_kwh' - 'avg_power_recorded_kw' - 'cost_recorded' - 'cost_applied'
	                           - 'receipt_started_at' - 'receipt_ended_at',
	       updated_at = NOW()
	 WHERE metadata->>'energy_source' = 'rivian_receipt' AND `

// energyStore is what applyEnergy needs on top of store; *Store implements it.
type energyStore interface {
	EnergyCandidates(ctx context.Context, uid uuid.UUID, since time.Time) ([]EnergyCand, error)
	ApplyEnergy(ctx context.Context, uid uuid.UUID, c EnergyCand, up EnergyUpdate) (bool, error)
}

// SetEnergy sets the energy mode and the observer of its outcomes (outcome, delta percent; the percent
// is NaN-free and 0 when not computed). Default (never called) is off.
func (s *Syncer) SetEnergy(mode string, observe func(outcome string, deltaPct float64)) {
	s.energyMode, s.energyObserve = mode, observe
}

// applyEnergy looks at the user's recently matched receipts. In observe mode it only counts, once per
// receipt and energy; in apply mode it writes the ones that pass.
func (s *Syncer) applyEnergy(ctx context.Context, uid uuid.UUID) error {
	es, ok := s.st.(energyStore)
	if !ok || s.energyMode == "" || s.energyMode == EnergyOff || !s.still(uid) {
		return nil
	}
	cands, err := es.EnergyCandidates(ctx, uid, time.Now().Add(-energyWindow))
	if err != nil {
		return err
	}
	for _, c := range cands {
		outcome, pct, up := EvaluateEnergy(c)
		if outcome == OutcomeWouldApply && up == nil {
			continue // already in
		}
		if s.energyMode == EnergyApply && up != nil && outcome == OutcomeWouldApply {
			done, err := es.ApplyEnergy(ctx, uid, c, *up)
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			outcome = OutcomeApplied
		}
		if s.energySeen(c, outcome) {
			// One line per receipt and outcome, no user data: the apply gate counts distinct receipts from it.
			s.logger.Info("receipt energy", "receipt_id", c.ReceiptID.String(), "outcome", outcome, "delta_pct", math.Round(pct*100)/100, "mode", s.energyMode)
			if s.energyObserve != nil {
				s.energyObserve(outcome, pct)
			}
		}
	}
	return nil
}

// energySeen reports whether this receipt/outcome pair is new, so a pass that re-reads the same rows
// does not count them again.
func (s *Syncer) energySeen(c EnergyCand, outcome string) bool {
	k := energyKey{c.ReceiptID, outcome}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.energyDone == nil || len(s.energyDone) > 20000 {
		s.energyDone = map[energyKey]struct{}{}
	}
	if _, dup := s.energyDone[k]; dup {
		return false
	}
	s.energyDone[k] = struct{}{}
	return true
}

type energyKey struct {
	receipt uuid.UUID
	outcome string
}
