package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

// "Your year in Rivolt": a read-only card of the signed-in user's own calendar year (UTC), computed here so the web and the
// iPhone show the same figures. Only what DA approved is in the answer: miles, kWh, charge count, the best single drive's
// efficiency (no date, no place), the longest trip (miles and month), the coldest drive (temperature and month, only when
// weather was recorded) and the cost with an estimate flag. No coordinates, places, dates finer than a month, or ids.
//
// Gates: at least yearMinDrives drives and yearMinCharges charges in the year, and an account at least yearMinAccountAge old.
// When a gate fails the answer is {"eligible":false,"reason":...} and carries no figures, so a card cannot be drawn by accident.

const (
	yearMinDrives     = 10
	yearMinCharges    = 3
	yearMinAccountAge = 30 * 24 * time.Hour
	// A drive shorter than this says nothing about efficiency (a parking-lot shuffle can read 9 mi/kWh).
	yearMinEfficiencyMi = 5.0
	yearMaxRows         = 5000
)

type yearMonthMiles struct {
	Miles float64 `json:"miles"`
	Month string  `json:"month"` // "2026-03"
}

type yearColdest struct {
	TempF float64 `json:"temp_f"`
	Month string  `json:"month"`
}

type yearCost struct {
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	Estimated bool    `json:"estimated"` // some of the amount is an estimate (no billed or entered price)
}

// yearResponse is GET /api/year-in-rivolt[?year=YYYY]. Ineligible answers set only Eligible, Year and Reason.
type yearResponse struct {
	Eligible bool   `json:"eligible"`
	Year     int    `json:"year"`
	Reason   string `json:"reason,omitempty"` // "account_too_new" | "not_enough_activity"

	Miles        float64         `json:"miles,omitempty"`
	KWh          float64         `json:"kwh,omitempty"` // energy the drives used
	ChargeCount  int             `json:"charge_count,omitempty"`
	BestMiPerKWh float64         `json:"best_efficiency_mi_per_kwh,omitempty"`
	LongestTrip  *yearMonthMiles `json:"longest_trip,omitempty"`
	ColdestDrive *yearColdest    `json:"coldest_drive,omitempty"`
	Cost         *yearCost       `json:"cost,omitempty"`
}

// yearCharge is the part of a charge the card needs.
type yearCharge struct {
	cost      float64
	estimated float64
	currency  string
	energyEst bool
}

// yearGate is "" when the card may be drawn, else why not.
func yearGate(now, createdAt time.Time, nDrives, nCharges int) string {
	switch {
	case now.Sub(createdAt) < yearMinAccountAge:
		return "account_too_new"
	case nDrives < yearMinDrives || nCharges < yearMinCharges:
		return "not_enough_activity"
	}
	return ""
}

// computeYear is the pure part: rows already limited to the year. coldest is nil when no weather was recorded.
func computeYear(year int, ds []drives.Drive, cs []yearCharge, coldest *yearColdest) yearResponse {
	out := yearResponse{Eligible: true, Year: year, ChargeCount: len(cs), ColdestDrive: coldest}
	var longest drives.Drive
	for _, d := range ds {
		out.Miles += d.DistanceMi
		out.KWh += d.EnergyUsedKWh
		if d.DistanceMi >= yearMinEfficiencyMi && d.EnergyUsedKWh > 0 {
			if e := d.DistanceMi / d.EnergyUsedKWh; e > out.BestMiPerKWh {
				out.BestMiPerKWh = e
			}
		}
		if d.DistanceMi > longest.DistanceMi {
			longest = d
		}
	}
	if longest.DistanceMi > 0 {
		out.LongestTrip = &yearMonthMiles{Miles: longest.DistanceMi, Month: longest.StartedAt.UTC().Format("2006-01")}
	}
	// One currency per card: the one that carries the most spend. Charges in another currency are left out of the sum.
	spend := map[string]float64{}
	for _, c := range cs {
		amt := c.cost
		if amt <= 0 {
			amt = c.estimated
		}
		spend[c.currency] += amt
	}
	best, bestAmt := "", -1.0
	for cur, amt := range spend {
		if amt > bestAmt || (amt == bestAmt && cur < best) {
			best, bestAmt = cur, amt
		}
	}
	if bestAmt > 0 {
		cost := &yearCost{Currency: best}
		for _, c := range cs {
			if c.currency != best {
				continue
			}
			if c.cost > 0 {
				cost.Amount += c.cost
				cost.Estimated = cost.Estimated || c.energyEst
			} else if c.estimated > 0 {
				cost.Amount += c.estimated
				cost.Estimated = true
			}
		}
		out.Cost = cost
	}
	return out
}

func handleYearInRivolt(pool *sql.DB, uid uuid.UUID, ds *drives.Store, cs *charges.Store, settingsStore *settings.Store, billed func(context.Context) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		now := time.Now().UTC()
		year := now.Year()
		if s := r.URL.Query().Get("year"); s != "" {
			y, err := strconv.Atoi(s)
			if err != nil || (y != now.Year() && y != now.Year()-1) {
				http.Error(w, "year must be this year or last year", http.StatusBadRequest)
				return
			}
			year = y
		}
		resp := func(v yearResponse) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		ctx := r.Context()
		var createdAt time.Time
		if pool == nil || pool.QueryRowContext(ctx, `SELECT created_at FROM users WHERE id = $1`, uid).Scan(&createdAt) != nil {
			http.Error(w, "could not read the account", http.StatusInternalServerError)
			return
		}
		if reason := yearGate(now, createdAt, yearMinDrives, yearMinCharges); reason != "" {
			resp(yearResponse{Year: year, Reason: reason})
			return
		}
		from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(1, 0, 0)
		var yd []drives.Drive
		if ds != nil {
			rows, err := ds.ListRecentForVehicle(ctx, "", yearMaxRows, false)
			if err != nil {
				http.Error(w, "could not read drives", http.StatusInternalServerError)
				return
			}
			for _, d := range drives.FilterReparking(rows) {
				if !d.StartedAt.Before(from) && d.StartedAt.Before(to) {
					yd = append(yd, d)
				}
			}
		}
		var yc []yearCharge
		if cs != nil {
			rows, err := cs.ListRecent(ctx, yearMaxRows)
			if err != nil {
				http.Error(w, "could not read charges", http.StatusInternalServerError)
				return
			}
			for _, c := range decorateCharges(ctx, rows, settingsStore, billed != nil && billed(ctx)) {
				if c.StartedAt.Before(from) || !c.StartedAt.Before(to) {
					continue
				}
				cur := c.Currency
				if c.Cost <= 0 {
					cur = c.EstimatedCurrency
				}
				yc = append(yc, yearCharge{cost: c.Cost, estimated: c.EstimatedCost, currency: cur, energyEst: c.EnergyEstimated})
			}
		}
		if reason := yearGate(now, createdAt, len(yd), len(yc)); reason != "" {
			resp(yearResponse{Year: year, Reason: reason})
			return
		}
		var coldest *yearColdest
		var tc float64
		var at time.Time
		err := pool.QueryRowContext(ctx, `
SELECT temp_c, sampled_at FROM drive_weather
WHERE user_id = $1 AND temp_c IS NOT NULL AND sampled_at >= $2 AND sampled_at < $3
ORDER BY temp_c ASC LIMIT 1`, uid, from, to).Scan(&tc, &at)
		if err == nil {
			coldest = &yearColdest{TempF: tc*1.8 + 32, Month: at.UTC().Format("2006-01")}
		} else if err != sql.ErrNoRows {
			http.Error(w, "could not read weather", http.StatusInternalServerError)
			return
		}
		resp(computeYear(year, yd, yc, coldest))
	}
}
