// Package receipts syncs what Rivian actually billed for each charging
// session (its Charging history) next to our own estimate.
package receipts

import (
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ChargeRef is the part of a charge row the matcher looks at.
type ChargeRef struct {
	ID        uuid.UUID
	StartedAt time.Time
	EndedAt   time.Time
	EnergyKWh float64
}

// Receipt is one Rivian charging-history row.
type Receipt struct {
	ID            uuid.UUID
	TransactionID string
	StartedAt     time.Time
	EndedAt       time.Time
	TotalCost     *float64
	Currency      string
	EnergyKWh     *float64
	RangeAddedKm  *float64
	Vendor        string
	City          string
	ChargerName   string
	IsHome        *bool
	DetailFetched bool
	ChargeID      *uuid.UUID
}

// Pair says which charge a receipt belongs to.
type Pair struct {
	ReceiptID uuid.UUID
	ChargeID  uuid.UUID
}

const (
	// matchWindow is how far a receipt's start (or end) may sit from the
	// charge's: our recorder closes a session on plug-out or the terminal
	// state, Rivian on its own clock.
	matchWindow = 5 * time.Minute
	// energyTolAbs / energyTolRel: the energies must agree within the
	// larger of 1 kWh and 10 %.
	energyTolAbs = 1.0
	energyTolRel = 0.10
)

// Match pairs unmatched receipts with unmatched charges one to one.
//
// A receipt and a charge are candidates when both their starts and their
// ends are within 5 minutes, or when one of the two is within 5 minutes
// and the energies agree (a long trickle session can end at different
// moments on the two clocks). When both energies are known and disagree
// the pair is never a candidate, however close the times. Candidates are
// taken best first by |start gap| + |end gap|.
func Match(charges []ChargeRef, receipts []Receipt) []Pair {
	type cand struct {
		ri, ci int
		score  time.Duration
	}
	var cands []cand
	for ri, r := range receipts {
		if r.ChargeID != nil {
			continue
		}
		for ci, c := range charges {
			ds, de := absDur(r.StartedAt.Sub(c.StartedAt)), absDur(r.EndedAt.Sub(c.EndedAt))
			bothClose := ds <= matchWindow && de <= matchWindow
			oneClose := ds <= matchWindow || de <= matchWindow
			if !oneClose {
				continue
			}
			known := r.EnergyKWh != nil && *r.EnergyKWh > 0 && c.EnergyKWh > 0
			if known && !energyAgrees(*r.EnergyKWh, c.EnergyKWh) {
				continue
			}
			if !bothClose && !known {
				continue
			}
			cands = append(cands, cand{ri, ci, ds + de})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score < cands[j].score })
	usedR := map[int]bool{}
	usedC := map[int]bool{}
	var out []Pair
	for _, c := range cands {
		if usedR[c.ri] || usedC[c.ci] {
			continue
		}
		usedR[c.ri], usedC[c.ci] = true, true
		out = append(out, Pair{ReceiptID: receipts[c.ri].ID, ChargeID: charges[c.ci].ID})
	}
	return out
}

func energyAgrees(a, b float64) bool {
	tol := math.Max(energyTolAbs, energyTolRel*math.Max(a, b))
	return math.Abs(a-b) <= tol
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
